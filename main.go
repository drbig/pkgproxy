// See LICENSE.txt for licensing information.

// pkgproxy is a caching transparent HTTP proxy intended to save time and bandwidth
// spent on upgrading OS installations.
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
	VERSION = `0.0.2` // current version
)

var (
	client      = &http.Client{}                          // used for upstream requests
	filters     []*regexp.Regexp                          // holds regexp filters applied to RequestURI
	rangeRegexp = regexp.MustCompile(`bytes=(\d*)-(\d*)`) // for basic support of partial content requests
	flagRoot    string                                    // cache root directory
	flagAddr    string                                    // proxy bind address
	flagFilters string                                    // path to filters file
	reqNum      int                                       // last incoming request id
	mtxNum      sync.Mutex                                // mutex for the above
)

var (
	cntDown  = expvar.NewInt("statsDownBytes")  // counts significant bytes downloaded from upstream
	cntCache = expvar.NewInt("statsCacheBytes") // counts significant bytes served from cached files
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
	log.Println("Cache root at", flagRoot)

	if flagFilters != "" {
		if err = loadFilters(flagFilters); err != nil {
			os.Exit(1)
		}
	} else {
		log.Println("No filters file given")
	}

	http.HandleFunc("/", handle)
	log.Println("Starting proxy server at", flagAddr)
	go log.Fatalln(http.ListenAndServe(flagAddr, nil))

	sigwait()
}

// handle processes the incoming request.
// The general goal is that if anything related to the local cache fails the
// incoming request should be passed upstream.
func handle(w http.ResponseWriter, req *http.Request) {
	id := fmt.Sprintf("[%3d]", getReqNum())
	log.Println(id, req.RemoteAddr, "requests", req.URL.Path)

	if f := hasCached(id, req.URL); f != nil {
		d, err := tryServeCached(id, w, req, f)
		if err != nil {
			log.Println(id, err)
		}
		if d {
			return
		}
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
			defer barrierSet(false, p)
			defer f.Close()
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

// loadFilters tries to open the filters file and parse it.
// If the file can't be opened the previously set filters (if any) will
// still be there.
func loadFilters(path string) error {
	f, err := os.Open(path)
	if err != nil {
		log.Println(err)
		return err
	}
	defer f.Close()
	filters = make([]*regexp.Regexp, 0)
	parseFilters(f)
	log.Println("Parsed", len(filters), "filters at", path)
	return nil
}

// parseFilters tries to parse and set filter regexps.
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

// getReqNum returns the current request id.
// The request ids are always between 1 - 999.
func getReqNum() int {
	mtxNum.Lock()
	defer mtxNum.Unlock()
	reqNum++
	if reqNum > 999 {
		reqNum = 1
	}
	return reqNum
}

// hasCached checks if we have a complete file for the given path, and if so
// returns an opened file handle.
func hasCached(id string, url *url.URL) *os.File {
	p := filepath.Join(flagRoot, url.Path)
	if barrierCheck(p) {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		} else {
			log.Println(id, err)
			return nil
		}
	}
	return f
}

// tryServeCached tries to serve content from a cached file.
// It handles single-range partial content requests.
func tryServeCached(id string, w http.ResponseWriter, req *http.Request, f *os.File) (bool, error) {
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, err
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
				return false, err
			}
			to++
			if to > fi.Size() {
				return false, fmt.Errorf("Bad range %s (%d > %d)\n", rs, to, l)
			}
			l = to
		}
		if ms[1] != "" {
			fr, err = strconv.ParseInt(ms[1], 10, 64)
			if err != nil {
				return false, err
			}
			if fr > l {
				return false, fmt.Errorf("Bad range %s (%d > %d)\n", rs, fr, l)
			}
			if _, err := f.Seek(fr, 0); err != nil {
				return false, err
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
	cntCache.Add(n)
	return true, err
}

// shouldCache checks if the upstream data should be saved to the local cache.
// It won't cache partial requests, stuff that matches the filters, or if the
// data is already being cached to disk.
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
	if barrierCheck(p) {
		return "", false
	}
	return p, true
}

// requestUpstream tries to pass the incoming request to the real target.
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

// prepFile tries to setup a file for the upstream data to cache.
func prepFile(url *url.URL) (*os.File, error) {
	p := filepath.Join(flagRoot, url.Path)
	barrierSet(true, p)
	if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
		barrierSet(false, p)
		return nil, err
	}
	f, err := os.Create(p)
	if err != nil {
		barrierSet(false, p)
		return nil, err
	}
	return f, nil
}
