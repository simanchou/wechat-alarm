package main

import (
	"fmt"
	"github.com/bitly/go-simplejson"
	"github.com/go-ini/ini"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	TimeFormat        = "2006-01-02 15:04:05"
	TimeFormatISODate = "2006-01-02T15:04:05.999999999-07:00"
)

type Conf struct {
	CorpID     string
	CorpSecret string
	AgentID    string
	Level      map[string]string
}

var conf = Conf{}

func init() {
	cfg, err := ini.Load("wechat-alarm.ini")
	if err != nil {
		log.Fatalln("load conf file fail, check it again")
	}

	conf.CorpID = cfg.Section("wechat").Key("corpid").String()
	conf.CorpSecret = cfg.Section("wechat").Key("corpsecret").String()
	conf.AgentID = cfg.Section("wechat").Key("agentid").String()
	conf.Level = map[string]string{
		"1": cfg.Section("level").Key("1").String(),
		"2": cfg.Section("level").Key("2").String(),
		"3": cfg.Section("level").Key("3").String()}
}

func main() {
	alertReceive := &AlertMSG{MCH: make(chan string, 1000),
		CorpID:     conf.CorpID,
		CorpSecret: conf.CorpSecret,
		AgentID:    conf.AgentID}
	go func() {
		for {
			alertReceive.sendToWeChat()
		}
	}()

	http.HandleFunc("/", alertReceive.handleReceive)
	http.ListenAndServe(":9000", nil)
}

// Receive alert msg to channel
type AlertMSG struct {
	MCH                chan string
	CorpID             string
	CorpSecret         string
	AgentID            string
	AccessToken        string
	AccessTokenCreatAt time.Time
}

func (m *AlertMSG) handleReceive(w http.ResponseWriter, r *http.Request) {
	s, _ := ioutil.ReadAll(r.Body)
	m.MCH <- fmt.Sprintf("%s", s)
	log.Printf("receive alert message from alertmanager, %s\n", s)
}

func (m *AlertMSG) sendToWeChat() {

	s := <-m.MCH

	json, err := simplejson.NewJson([]byte(s))
	if err != nil {
		log.Fatalln("error json")
	}
	alerts, _ := json.Get("alerts").Array()

	for i := range alerts {
		alert := json.Get("alerts").GetIndex(i)
		status := alert.Get("status").MustString()
		alertName := alert.Get("labels").Get("alertname").MustString()
		hostName := alert.Get("labels").Get("hostname").MustString()
		env := alert.Get("labels").Get("env").MustString()
		job := alert.Get("labels").Get("job").MustString()
		project := alert.Get("labels").Get("project").MustString()
		service := alert.Get("labels").Get("service").MustString()
		level := alert.Get("labels").Get("level").MustString()
		startsAt := alert.Get("startsAt").MustString()
		currentTime := time.Now()

		startsTime, _ := time.Parse(TimeFormatISODate, startsAt)
		toUser := conf.Level[level]
		msg := fmt.Sprintf(`{
   "touser" : "%s",
   "toparty" : "PartyID1 | PartyID2",
   "totag" : "TagID1 | TagID2",
   "msgtype" : "text",
   "agentid" : 1000002,
   "text" : {
		"content" : "[%s]-%s</br>Project: %s</br>Env: %s</br>Hostname: %s</br>Job: %s</br>Service: %s</br>Level: %s</br>StartAt: %s</br>SendAt: %s"
   }
}`, toUser, status, alertName, project, env, hostName, job, service, level, startsTime.Format(TimeFormat), currentTime.Format(TimeFormat))
		m.Send(msg)
		// wechat have rate limit,so we need to wait a second for next send
		time.Sleep(1 * time.Second)
	}

}

func (m *AlertMSG) GetAccessToken() {
	log.Printf("start to get access token")
	api := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		m.CorpID, m.CorpSecret)
	resp, err := http.Get(api)
	if err != nil {
		log.Printf("connect to wechat acces token api fail, error: %s", err)
		log.Printf("reconnect to wechat access token api after 5 seconds")
		time.Sleep(5 * time.Second)
		m.GetAccessToken()
	} else {
		log.Printf("connect to wechat access token api successful")
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("read from wechat access token api fail, error: %s", err)
		log.Printf("reconnect to wechat access token api after 5 seconds")
		time.Sleep(5 * time.Second)
		m.GetAccessToken()
	} else {
		log.Printf("read from wechat access token api successful")
	}

	json, err := simplejson.NewJson(body)
	errcode := json.Get("errcode").MustInt()
	errmsg := json.Get("errmsg").MustString()
	if errcode == 0 {
		m.AccessToken = json.Get("access_token").MustString()
		m.AccessTokenCreatAt = time.Now()
		log.Printf("get wechat access token successful, %s", m.AccessToken)
	} else {
		log.Printf("get wechat access token fail, %s", errmsg)
		log.Printf("try to get wechat access token again after 5 seconds")
		time.Sleep(5 * time.Second)
		m.GetAccessToken()
	}
}

func (m *AlertMSG) Send(msg string) {
	log.Printf("check wechat access token is timeout or not")
	currentTime := time.Now()
	timeoutDuration, _ := time.ParseDuration("2h")
	if currentTime.After(m.AccessTokenCreatAt.Add(timeoutDuration)) {
		log.Printf("wechat access token have been timeout before %s, get new access token now, please wait\n",
			m.AccessTokenCreatAt.Add(timeoutDuration).Sub(currentTime))
		m.GetAccessToken()
	} else {
		log.Printf("wechat access token is not timeout")
	}
	log.Printf("start to send alert massage to wechat")

	api := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=%s", m.AccessToken)
	resp, err := http.Post(api, "application/x-www-form-urlencoded", strings.NewReader(msg))
	if err != nil {
		log.Printf("connect to wechat send message api fail, error: %s, retry after 5 seconds", err)
		time.Sleep(5 * time.Second)
		m.Send(msg)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("read from wechat send message api fail, error: %s, retry after 5 seconds", err)
		time.Sleep(5 * time.Second)
		m.Send(msg)
	}

	json, _ := simplejson.NewJson(body)
	errcode := json.Get("errcode").MustInt()
	errmsg := json.Get("errmsg").MustString()
	invalidateUser := json.Get("invaliduser").MustString()
	if errcode == 0 {
		log.Printf("alert message send to wechat successful, %s", msg)
	} else {
		log.Printf("alert message send to wechat fail, error: %s", errmsg)
	}
	if len(invalidateUser) > 0 {
		log.Printf("alert message send to [ %s ] fail, invalidate user", invalidateUser)
	}
}
