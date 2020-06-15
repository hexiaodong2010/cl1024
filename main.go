package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	_ "golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	_ "golang.org/x/text/transform"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type urlJob struct {
	Images         []string
	Title          string
	Url            string
	Finished       bool
	HttpStatusCode uint8
}

var (
	finishedNum      = uint32(0)
	host             string
	httpProxy        string
	download         bool
	dir              string
	page             int
	err              error
	hostObject       *url.URL
	jobs             = make(map[string]*urlJob)
	pipeLimit        = make(chan bool, 5)
	jobsRw           sync.RWMutex
	jsonFile         = "./jobs.json"
	saveJobsJsonOnce = &sync.Once{}

	jobPool = &JobPoolChannel{
		C:        make(chan *urlJob, 2),
		once:     sync.Once{},
		IsClosed: false,
	}
)

type JobPoolChannel struct {
	C        chan *urlJob
	once     sync.Once
	IsClosed bool
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			log.Println("recover")
			log.Printf("%s\n", err)
		}
		saveJobsJsonOnce.Do(func() {
			bys, _ := json.Marshal(&jobs)
			ioutil.WriteFile(jsonFile, bys, os.ModePerm)
			log.Println("end main process")
		})
		//log.Println()(jobs)
	}()
	//图片区链接
	//"https://cb.bbcb.xyz/thread0806.php?fid=16"
	//内容页面
	//htm_data/1109/16/594741.html <h3><a href="htm_data/2006/16/3967138.html" target="_blank" id="">[原创] [手势认证] 精厕小狐狸露出系列⑥，露脸。 [20P]</a></h3>
	flag.StringVar(&host, "host", "https://cb.bbcb.xyz/thread0806.php?fid=16", "***/thread0806.php?fid=16&search=&page=1")
	flag.StringVar(&dir, "dir", "./data", "image data dir eg:./data c:/data")
	flag.StringVar(&httpProxy, "httpProxy", "", "httpProxy eg:http://127.0.0.1:8080")
	flag.IntVar(&page, "page", 1, "page in url query page")
	flag.BoolVar(&download, "d", true, "download switch , switch on,download image else collect image urls")
	flag.Parse()
	hostObject, err = url.Parse(host)
	watchChan := make(chan bool)
	go func() {
		for {
			select {
			case <-watchChan:
				watching()
			}
		}
	}()
	loadJobsFromJson()
	go func() {
		for {
			if job, ok := <-jobPool.C; ok {
				pipeLimit <- true
				go func(job *urlJob) {
					if len(job.Images) < 1 {
						job.Images = getImageListsByUrl(job.Url)
					}

					defer func() {
						atomic.AddUint32(&finishedNum, 1)
						watchChan <- true
						<-pipeLimit
					}()
					if job.Finished {
						return
					}
					if download == false {
						return
					}
					log.Println("开始执行下载：", job.Title)
					for _, imgUrl := range job.Images {
						downloadImage(imgUrl, job)
					}
					jobsRw.Lock()
					defer jobsRw.Unlock()
					jobs[job.Url].Finished = true
				}(job)
			} else {
				log.Println("程序执行完毕")
				break
			}
		}
	}()
	newQuery := hostObject.Query()
	newQuery.Set("search", "")
	newQuery.Set("page", strconv.Itoa(page))
	hostObject.RawQuery = newQuery.Encode()
	getListPageUrls(hostObject.String())
}
func getImageListsByUrl(url string) []string {
	log.Println("获取图片下载链接 start ", url)
	content := doRequest(url)
	reg := regexp.MustCompile(`ess-data='(.*?)'>`)
	regs := reg.FindAllStringSubmatch(content, -1)
	if len(regs) < 1 {
		return nil
	}
	var res []string
	for _, v := range regs {
		res = append(res, v[1])
	}
	log.Println("获取图片下载链接 end")
	return res
}
func doRequest(urlStr string) string {
	time.Sleep(3 * time.Second)
	client := http.DefaultClient
	if len(httpProxy) > 0 {
		proxyURL, _ := url.Parse(httpProxy)
		trans := &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
		client = &http.Client{
			Transport: trans,
		}
	}
	resp, _ := client.Get(urlStr)
	if resp.StatusCode != http.StatusOK {
		log.Fatal("http request error,errorCode" + strconv.Itoa(resp.StatusCode))
	}
	body, _ := ioutil.ReadAll(transform.NewReader(resp.Body, simplifiedchinese.GBK.NewDecoder()))
	return string(body)
}
func getListPageUrls(url string) {
	log.Println("获取列表信息 start")
	listBodyString := doRequest(url)

	regList, _ := regexp.Compile(`<a href="htm_data/(.*?).html" target="_blank" id="">(.*?)</a>`)
	lists := regList.FindAllStringSubmatch(listBodyString, -1)

	for _, item := range lists {
		urlString := fmt.Sprintf(`%s://%s/htm_data/%s.html`, hostObject.Scheme, hostObject.Host, item[1])
		title := trimHtml(item[2])
		//判断是否是图片链接 图片链接标题有 数字+P
		reg := regexp.MustCompile(`\d{1,3}P`)
		if !reg.MatchString(title) {
			continue
		}
		if _, ok := jobs[urlString]; !ok {
			log.Println("添加任务下载：", title)
			if len(jobs) > 0 {
				//return
			}
			jobs[urlString] = &urlJob{
				Title:          title,
				Url:            urlString,
				Finished:       false,
				HttpStatusCode: 0,
				//Images:         getImageListsByUrl(urlString),
			}
		}
	}

	for _, job := range jobs {
		jobPool.C <- job
	}
}

func trimHtml(src string) string {
	//将HTML标签全转换成小写
	re, _ := regexp.Compile("\\<[\\S\\s]+?\\>")
	src = re.ReplaceAllStringFunc(src, strings.ToLower)
	//去除STYLE
	re, _ = regexp.Compile("\\<style[\\S\\s]+?\\</style\\>")
	src = re.ReplaceAllString(src, "")
	//去除SCRIPT
	re, _ = regexp.Compile("\\<script[\\S\\s]+?\\</script\\>")
	src = re.ReplaceAllString(src, "")
	//去除所有尖括号内的HTML代码，并换成换行符
	re, _ = regexp.Compile("\\<[\\S\\s]+?\\>")
	src = re.ReplaceAllString(src, "\n")
	//去除连续的换行符
	re, _ = regexp.Compile("\\s{2,}")
	src = re.ReplaceAllString(src, "\n")
	return strings.TrimSpace(src)
}
func downloadImage(testImg string, job *urlJob) {
	reg := regexp.MustCompile(`(\w|\d|_)*.(jp\w{1,2}|png|gif)`)
	log.Println("开始处理图片：", job.Title, testImg)
	_ = os.Mkdir(fmt.Sprintf(`%s/%s`,dir,job.Title), os.ModePerm)
	name := fmt.Sprintf(`%s/%s_%s`, dir, job.Title, reg.FindStringSubmatch(strings.ToLower(testImg))[0])
	fileInfo, err := os.Stat(name)
	if err == nil && fileInfo.Size() > 0 {
		log.Println("文件已经存在")
		return
	}
	file, _ := os.Create(name)
	defer file.Close()
	resp, _ := http.Get(testImg)
	body, _ := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()

	_, _ = io.Copy(file, bytes.NewReader(body))
}

func loadJobsFromJson() {
	_ = os.Mkdir(dir, os.ModePerm)
	jobsRw.Lock()
	defer jobsRw.Unlock()
	bs, err := ioutil.ReadFile(jsonFile)
	if err == nil {
		json.Unmarshal(bs, &jobs)
	}
}

func watching() {
	log.Printf("当前执行任务数量: %d，已完成任务%d,总任务数量%d\n", runtime.NumGoroutine(), finishedNum, len(jobs))
}
