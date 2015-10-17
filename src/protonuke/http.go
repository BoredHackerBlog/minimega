// Copyright (2015) Sandia Corporation.
// Under the terms of Contract DE-AC04-94AL85000 with Sandia Corporation,
// the U.S. Government retains certain rights in this software.

package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"image"
	"image/png"
	"io"
	"io/ioutil"
	"math/rand"
	log "minilog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	MAX_CACHE = 128
)

var (
	htmlTemplate     *template.Template
	hits             uint64
	hitsTLS          uint64
	hitChan          chan uint64
	hitTLSChan       chan uint64
	httpSiteCache    []string
	httpTLSSiteCache []string
	httpImage        []byte
	httpReady        bool
	httpLock         sync.Mutex
	httpFS           http.Handler
)

type HtmlContent struct {
	URLs   []string
	Hits   uint64
	URI    string
	Secure bool
	Host   string
}

func httpClient(protocol string) {
	log.Debugln("httpClient")

	t := NewEventTicker(*f_mean, *f_stddev, *f_min, *f_max)

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: func(network, addr string) (net.Conn, error) {
			return net.Dial(protocol, addr)
		},
	}

	client := &http.Client{
		Transport: transport,
	}

	for {
		t.Tick()
		h, o := randomHost()
		log.Debug("http host %v from %v", h, o)
		elapsed := httpClientRequest(h, client)
		if elapsed != 0 {
			log.Info("http %v %vns", h, elapsed)
		}
		httpReportChan <- 1
	}
}

func httpTLSClient(protocol string) {
	log.Debugln("httpTLSClient")

	t := NewEventTicker(*f_mean, *f_stddev, *f_min, *f_max)

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           http.ProxyFromEnvironment,
		Dial: func(network, addr string) (net.Conn, error) {
			return net.Dial(protocol, addr)
		},
	}

	client := &http.Client{
		Transport: transport,
	}

	for {
		t.Tick()
		h, o := randomHost()
		log.Debug("https host %v from %v", h, o)
		elapsed := httpTLSClientRequest(h, client)
		if elapsed != 0 {
			log.Info("https %v %vns", client, elapsed)
		}
		httpTLSReportChan <- 1
	}
}

func httpClientRequest(h string, client *http.Client) (elapsed uint64) {
	httpSiteCache = append(httpSiteCache, h)
	if len(httpSiteCache) > MAX_CACHE {
		httpSiteCache = httpSiteCache[len(httpSiteCache)-MAX_CACHE:]
	}

	s := rand.NewSource(time.Now().UnixNano())
	r := rand.New(s)
	url := httpSiteCache[r.Int31()%int32(len(httpSiteCache))]

	log.Debugln("http using url: ", url)

	// url notation requires leading and trailing [] on ipv6 addresses
	if isIPv6(url) {
		url = "[" + url + "]"
	}

	if !strings.HasPrefix(url, "http://") {
		url = "http://" + url
	}

	start := time.Now().UnixNano()
	resp, err := client.Get(url)
	if err != nil {
		log.Errorln(err)
		return
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	// make sure to grab any images, javascript, css
	extraFiles := parseBody(string(body))
	for _, v := range extraFiles {
		log.Debugln("grabbing extra file: ", v)
		httpGet(url, v, false, client)
	}

	links := parseLinks(string(body))
	if len(links) > 0 {
		httpSiteCache = append(httpSiteCache, links...)
		if len(httpSiteCache) > MAX_CACHE {
			httpSiteCache = httpSiteCache[len(httpSiteCache)-MAX_CACHE:]
		}
	}

	stop := time.Now().UnixNano()
	elapsed = uint64(stop - start)

	return
}

func httpTLSClientRequest(h string, client *http.Client) (elapsed uint64) {
	httpTLSSiteCache = append(httpTLSSiteCache, h)
	if len(httpTLSSiteCache) > MAX_CACHE {
		httpTLSSiteCache = httpTLSSiteCache[len(httpTLSSiteCache)-MAX_CACHE:]
	}

	s := rand.NewSource(time.Now().UnixNano())
	r := rand.New(s)
	url := httpTLSSiteCache[r.Int31()%int32(len(httpTLSSiteCache))]

	log.Debugln("https using url: ", url)

	// url notation requires leading and trailing [] on ipv6 addresses
	if isIPv6(url) {
		url = "[" + url + "]"
	}

	if !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	start := time.Now().UnixNano()
	resp, err := client.Get(url)
	if err != nil {
		log.Errorln(err)
		return
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	// make sure to grab any images, javascript, css
	extraFiles := parseBody(string(body))
	for _, v := range extraFiles {
		log.Debugln("grabbing extra file: ", v)
		httpGet(url, v, true, client)
	}

	links := parseLinks(string(body))
	if len(links) > 0 {
		httpTLSSiteCache = append(httpTLSSiteCache, links...)
		if len(httpTLSSiteCache) > MAX_CACHE {
			httpTLSSiteCache = httpTLSSiteCache[len(httpTLSSiteCache)-MAX_CACHE:]
		}
	}

	stop := time.Now().UnixNano()
	elapsed = uint64(stop - start)

	return
}

func httpGet(url, file string, useTLS bool, client *http.Client) {
	// url notation requires leading and trailing [] on ipv6 addresses
	if isIPv6(url) {
		url = "[" + url + "]"
	}

	if useTLS {
		if !strings.HasPrefix(file, "https://") {
			file = url + "/" + file
		}
		resp, err := client.Get(file)
		if err != nil {
			log.Errorln(err)
		} else {
			n, err := io.Copy(ioutil.Discard, resp.Body)
			if err != nil {
				log.Error("httpGet: %v, only copied %v bytes", err, n)
			}
			resp.Body.Close()
			httpTLSReportChan <- 1
		}
	} else {
		if !strings.HasPrefix(file, "http://") {
			file = url + "/" + file
		}
		resp, err := client.Get(file)
		if err != nil {
			log.Errorln(err)
		} else {
			n, err := io.Copy(ioutil.Discard, resp.Body)
			if err != nil {
				log.Error("httpGet: %v, only copied %v bytes", err, n)
			}
			resp.Body.Close()
			httpReportChan <- 1
		}
	}
}

func parseBody(body string) []string {
	var ret []string
	img := `src=[\'"]?([^\'" >]+)`

	images := regexp.MustCompile(img)
	i := images.FindAllStringSubmatch(body, -1)
	for _, v := range i {
		ret = append(ret, v[1])
	}

	log.Debugln("got extra files: ", ret)
	return ret
}

func parseLinks(body string) []string {
	var ret []string
	lnk := `href=[\'"]?([^\'" >]+)`

	links := regexp.MustCompile(lnk)
	i := links.FindAllStringSubmatch(body, -1)
	for _, v := range i {
		ret = append(ret, v[1])
	}

	log.Debugln("got links: ", ret)
	return ret
}

func httpSetup() {
	httpLock.Lock()
	defer httpLock.Unlock()

	if httpReady {
		return
	}
	httpReady = true

	if *f_httproot != "" {
		httpFS = http.FileServer(http.Dir(*f_httproot))
	}

	http.HandleFunc("/", httpHandler)
	httpMakeImage()
	http.HandleFunc("/image.png", httpImageHandler)

	var err error
	htmlTemplate, err = template.New("output").Parse(htmlsrc)
	if err != nil {
		log.Fatalln(err)
	}
}

func httpServer(p string) {
	log.Debugln("httpServer")
	httpSetup()
	hitChan = make(chan uint64, 1024)
	go hitCounter()
	server := &http.Server{
		Addr:    ":http",
		Handler: nil,
	}

	conn, err := net.Listen(p, ":http")
	if err != nil {
		log.Fatalln(err)
	}

	log.Fatalln(server.Serve(conn))
}

func httpTLSServer(p string) {
	log.Debugln("httpTLSServer")
	httpSetup()
	hitTLSChan = make(chan uint64, 1024)
	go hitTLSCounter()
	cert, key := generateCerts()

	//log.Fatalln(http.ListenAndServeTLS(":https", cert, key, nil))
	server := &http.Server{
		Addr:    ":https",
		Handler: nil,
	}
	config := &tls.Config{}
	if config.NextProtos == nil {
		config.NextProtos = []string{"http/1.1"}
	}

	var err error
	config.Certificates = make([]tls.Certificate, 1)
	config.Certificates[0], err = tls.LoadX509KeyPair(cert, key)
	if err != nil {
		log.Fatalln(err)
	}

	conn, err := net.Listen(p, ":https")
	if err != nil {
		log.Fatalln(err)
	}

	tlsListener := tls.NewListener(conn, config)
	log.Fatalln(server.Serve(tlsListener))
}

func httpMakeImage() {
	s := rand.NewSource(time.Now().UnixNano())
	r := rand.New(s)

	m := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	for i := 0; i < 1024*768; i++ {
		m.Pix[i] = uint8(r.Int())
	}

	b := new(bytes.Buffer)
	png.Encode(b, m)
	httpImage = b.Bytes()
}

func hitCounter() {
	for {
		c := <-hitChan
		hits++
		httpReportChan <- c
	}
}

func hitTLSCounter() {
	for {
		c := <-hitTLSChan
		hitsTLS++
		httpTLSReportChan <- c
	}
}

func httpImageHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now().UnixNano()
	w.Write(httpImage)
	stop := time.Now().UnixNano()
	elapsed := uint64(stop - start)
	if r.TLS != nil {
		log.Info("https %v %v %vns", r.RemoteAddr, r.URL, elapsed)
		hitTLSChan <- 1
	} else {
		log.Info("http %v %v %vns", r.RemoteAddr, r.URL, elapsed)
		hitChan <- 1
	}
}

func httpHandler(w http.ResponseWriter, r *http.Request) {
	log.Debug("request: %v %v", r.RemoteAddr, r.URL.String())
	var usingTLS bool
	if r.TLS != nil {
		log.Debugln("request using tls")
		usingTLS = true
	}

	start := time.Now().UnixNano()
	if httpFS != nil {
		httpFS.ServeHTTP(w, r)
	} else {
		h := &HtmlContent{
			URLs:   randomURLs(),
			Hits:   hits,
			URI:    fmt.Sprintf("%v %v", r.RemoteAddr, r.URL.String()),
			Host:   r.Host,
			Secure: usingTLS,
		}
		err := htmlTemplate.Execute(w, h)
		if err != nil {
			log.Errorln(err)
		}
	}

	stop := time.Now().UnixNano()
	elapsed := uint64(stop - start)

	if usingTLS {
		log.Info("https %v %v %vns", r.RemoteAddr, r.URL, elapsed)
		hitTLSChan <- 1
	} else {
		log.Info("http %v %v %vns", r.RemoteAddr, r.URL, elapsed)
		hitChan <- 1
	}
}

func randomURLs() []string {
	var ret []string
	s := rand.NewSource(time.Now().UnixNano())
	r := rand.New(s)
	for i := 0; i < 3; i++ {
		url := r.Int31()
		ret = append(ret, fmt.Sprintf("%v", url))

	}
	log.Debugln("random urls: ", ret)
	return ret
}

var htmlsrc = `
<h1>protonuke</h1>

<p>request URI: {{.URI}}</p>

<p>
{{range $v := .URLs}} 
<a href="http{{if $.Secure}}s{{end}}://{{$.Host}}/{{$v}}">{{$v}}</a><br>
{{end}}
</p>

<p>
hits: {{.Hits}}<br>
</p>

<p>
<img src=image.png>
</p>
`
