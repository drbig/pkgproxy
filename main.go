package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"time"

	"github.com/elazarl/goproxy"
)

var (
	cache       = make(map[string]bool)
	filters     []*regexp.Regexp
	flagRoot    string
	flagAddr    string
	flagVerbose bool
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options...] regexp regexp...\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.StringVar(&flagRoot, "r", "", "cache root directory")
	flag.StringVar(&flagAddr, "a", ":9999", "proxy bind address")
	flag.BoolVar(&flagVerbose, "v", false, "verbose output")
}

func main() {
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(0)
	}

	if flagRoot == "" {
		fr, err := os.Getwd()
		if err != nil {
			log.Fatalln(err)
			os.Exit(1)
		}
		flagRoot = fr
	}

	for _, arg := range flag.Args() {
		filters = append(filters, regexp.MustCompile(arg))
	}

	proxy := goproxy.NewProxyHttpServer()
	if flagVerbose {
		proxy.Verbose = true
	}
	proxy.OnRequest().DoFunc(
		func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			log.Println("Request", r.URL.Path)
			for _, re := range filters {
				if re.MatchString(r.URL.Path) {
					return r, nil
				}
			}
			for {
				if _, exist := cache[r.URL.Path]; !exist {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			filePath := path.Join(flagRoot, r.URL.Path)
			file, err := os.Open(filePath)
			if err != nil {
				if os.IsNotExist(err) {
					r.Header.Add("X-PkgCache", "fetch")
					return r, nil
				} else {
					log.Println(err)
					return r, nil
				}
			}

			resp := &http.Response{}
			resp.Request = r
			resp.TransferEncoding = r.TransferEncoding
			resp.Header = make(http.Header)
			resp.Header.Add("Content-Type", "application/octet-stream")
			resp.StatusCode = 200
			resp.Body = file
			log.Println("Used", r.URL.Path)

			return r, resp
		})
	proxy.OnResponse().DoFunc(
		func(r *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
			if r.Request.Header.Get("X-PkgCache") != "fetch" {
				return r
			}
			if r.StatusCode == 200 {
				cache[r.Request.URL.Path] = true
				defer func(key string) {
					delete(cache, key)
				}(r.Request.URL.Path)

				filePath := path.Join(flagRoot, r.Request.URL.Path)
				if err := os.MkdirAll(path.Dir(filePath), 0777); err != nil {
					log.Println(err)
					return r
				}
				file, err := os.Create(filePath)
				if err != nil {
					log.Println(err)
					return r
				}
				if n, err := io.Copy(file, r.Body); err != nil {
					log.Println(err)
				} else {
					log.Printf("Saved %s (%d)\n", filePath, n)
				}
				if err := file.Sync(); err != nil {
					log.Println(err)
				}
				if _, err := file.Seek(0, 0); err != nil {
					log.Println(err)
				}
				r.Body.Close()
				r.Body = file
			}
			return r
		})
	http.ListenAndServe(flagAddr, proxy)
}
