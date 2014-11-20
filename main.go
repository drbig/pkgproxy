package main

import (
	"bufio"
	"expvar"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
)

const (
	VERSION       = `0.0.1`
	partialSuffix = `.lock`
)

var (
	client      = &http.Client{}
	filters     []*regexp.Regexp
	rangeRegexp = regexp.MustCompile(`bytes=(\d*)-(\d*)`)
	flagRoot    string
	flagAddr    string
	flagFilters string
	reqNum      int
	mtx         sync.Mutex
)

var (
	cntDown  = expvar.NewInt("statsDownBytes")
	cntCache = expvar.NewInt("statsCacheBytes")
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options...]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.StringVar(&flagAddr, "a", ":9999", "proxy bind address")
	flag.StringVar(&flagFilters, "f", "", "path to regexp filters file")
	flag.StringVar(&flagRoot, "r", "", "cache root directory")
}

func main() {
	var err error

	flag.Parse()

	if flagRoot == "" {
		flagRoot, err = os.Getwd()
	} else {
		flagRoot, err = filepath.Abs(flagRoot)
	}
	if err != nil {
		log.Fatalln(err)
		os.Exit(1)
	}
	log.Println("Cache root:", flagRoot)

	if flagFilters != "" {
		if err = loadFilters(flagFilters); err != nil {
			os.Exit(1)
		}
	} else {
		log.Println("No filters file given")
	}

	http.HandleFunc("/", handle)
	log.Println("Starting proxy server at", flagAddr)
	log.Fatalln(http.ListenAndServe(flagAddr, nil))
}

func handle(w http.ResponseWriter, req *http.Request) {
	id := fmt.Sprintf("[%3d]", getReqNum())
	log.Println(id, req.RemoteAddr, "requests", req.URL.Path)

	if f := hasCached(req.URL); f != nil {
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			log.Println(id, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		l := fi.Size()
		w.Header().Set("Content-Type", "application/octet-stream")
		rs := req.Header.Get("Range")
		if rs != "" {
			var err error
			var fr int64
			to := l
			ms := rangeRegexp.FindStringSubmatch(rs)
			if ms[2] != "" {
				to, err = strconv.ParseInt(ms[2], 10, 64)
				if err != nil {
					log.Println(id, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				to++
				if to > fi.Size() {
					log.Printf("%s Bad range %s (%d > %d)\n", id, rs, to, l)
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				l = to
			}
			if ms[1] != "" {
				fr, err = strconv.ParseInt(ms[1], 10, 64)
				if err != nil {
					log.Println(id, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if fr > l {
					log.Printf("%s Bad range %s (%d > %d)\n", id, rs, fr, l)
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				if _, err := f.Seek(fr, 0); err != nil {
					log.Println(id, err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				l -= fr
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", fr, to, fi.Size()))
			w.Header().Set("Content-Length", strconv.FormatInt(l, 10))
			w.WriteHeader(http.StatusPartialContent)
			log.Printf("%s Using %s (partial: %s)\n", id, f.Name(), rs)
		} else {
			w.Header().Set("Content-Length", strconv.FormatInt(l, 10))
			w.WriteHeader(http.StatusOK)
			log.Println(id, "Using", f.Name())
		}
		n, err := io.CopyN(w, f, l)
		if err != nil {
			log.Println(id, err)
		}
		cntCache.Add(n)
		return
	}

	r, err := requestUpstream(req)
	if err != nil {
		log.Println(id, err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer r.Body.Close()
	rh := w.Header()
	for k, v := range r.Header {
		rh.Set(k, v[0])
	}
	w.WriteHeader(r.StatusCode)

	if r.StatusCode != 200 {
		if _, err := io.Copy(w, r.Body); err != nil {
			log.Println(id, err)
		}
		return
	}

	var o io.Writer
	o = w
	p, s := shouldCache(req)
	if s {
		f, err := prepFile(req.URL)
		if err != nil {
			log.Println(id, err)
		} else {
			defer func() {
				f.Close()
				if err := os.Remove(p + partialSuffix); err != nil {
					log.Println(id, err)
				}
			}()
			log.Println(id, "Saving", f.Name())
			o = io.MultiWriter(w, f)
		}
	}
	n, err := io.Copy(o, r.Body)
	if err != nil {
		log.Println(id, err)
		if s {
			defer func() {
				if err := os.Remove(p); err != nil {
					log.Println(id, err)
				}
			}()
		}
	}
	cntDown.Add(n)
	return
}

func loadFilters(path string) error {
	f, err := os.Open(path)
	if err != nil {
		log.Println(err)
		return err
	}
	defer f.Close()
	filters = make([]*regexp.Regexp, 0)
	parseFilters(f)
	log.Println("Parsed", len(filters), "filters")
	return nil
}

func parseFilters(input io.Reader) {
	s := bufio.NewScanner(input)
	for s.Scan() {
		r, err := regexp.Compile(s.Text())
		if err != nil {
			log.Println(err)
			continue
		}
		filters = append(filters, r)
	}
	return
}

func getReqNum() int {
	mtx.Lock()
	defer mtx.Unlock()
	reqNum++
	if reqNum > 999 {
		reqNum = 1
	}
	return reqNum
}

func hasCached(url *url.URL) *os.File {
	f, err := os.Open(filepath.Join(flagRoot, url.Path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		} else {
			log.Println(err)
			return nil
		}
	}
	return f
}

func shouldCache(req *http.Request) (string, bool) {
	if req.Header.Get("Range") != "" {
		return "", false
	}
	for _, r := range filters {
		if r.MatchString(req.RequestURI) {
			return "", false
		}
	}
	p := filepath.Join(flagRoot, req.URL.Path)
	if _, err := os.Stat(p + partialSuffix); os.IsExist(err) {
		return "", false
	}
	return p, true
}

func requestUpstream(req *http.Request) (*http.Response, error) {
	ureq, err := http.NewRequest(req.Method, req.RequestURI, req.Body)
	if err != nil {
		return nil, err
	}
	ures, err := client.Do(ureq)
	if err != nil {
		return nil, err
	}
	return ures, nil
}

func prepFile(url *url.URL) (*os.File, error) {
	p := filepath.Join(flagRoot, url.Path)
	if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
		return nil, err
	}
	l, err := os.Create(p + partialSuffix)
	if err != nil {
		return nil, err
	}
	l.Close()
	f, err := os.Create(p)
	if err != nil {
		if err := os.Remove(p + partialSuffix); err != nil {
			log.Println(err)
		}
		return nil, err
	}
	return f, nil
}
