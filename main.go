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
)

var (
	client   = &http.Client{}
	cache    = make(map[string]bool)
	filters  []*regexp.Regexp
	flagRoot string
	flagAddr string
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options...] regexp regexp...\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.StringVar(&flagRoot, "r", "", "cache root directory")
	flag.StringVar(&flagAddr, "a", ":9999", "proxy bind address")
}

func checkFile(path string) (*os.File, error) {
	for {
		if _, err := os.Stat(path + "-partial"); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		} else {
			return nil, err
		}
	}
	return file, nil
}

func handle(w http.ResponseWriter, req *http.Request) {
	log.Println("Request", req.URL.Path)
	filePath := path.Join(flagRoot, req.URL.Path)
	process := true
	download := false

	for _, re := range filters {
		if re.MatchString(req.URL.Path) {
			process = false
		}
	}

	if process {
		file, err := checkFile(filePath)
		if err != nil {
			log.Println(err)
		}
		if file == nil {
			download = true
		} else {
			defer file.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			log.Println("Using", filePath)
			if _, err := io.Copy(w, file); err != nil {
				log.Println(err)
			}
			return
		}
	}

	r, err := http.NewRequest(req.Method, req.RequestURI, nil)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	res, err := client.Do(r)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
	w.WriteHeader(res.StatusCode)
	if res.StatusCode != 200 {
		if _, err := io.Copy(w, res.Body); err != nil {
			log.Println(err)
		}
		return
	}
	var file, partial *os.File
	var target io.Writer
	target = w
	if download {
		if err := os.MkdirAll(path.Dir(filePath), 0777); err != nil {
			log.Println(err)
		} else {
			if partial, err = os.Create(filePath + "-partial"); err != nil {
				log.Println(err)
			} else {
				partial.Close()
				file, err = os.Create(filePath)
				if err != nil {
					log.Println(err)
				} else {
					log.Println("Saving", filePath)
					target = io.MultiWriter(w, file)
				}
			}
		}
	}
	if _, err := io.Copy(target, res.Body); err != nil {
		log.Println(err)
		if file != nil {
			file.Close()
			if err := os.Remove(filePath); err != nil {
				log.Println(err)
			}
		}
	} else {
		if file != nil {
			file.Close()
		}
	}
	if partial != nil {
		if err := os.Remove(filePath + "-partial"); err != nil {
			log.Println(err)
		}
	}
	return
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

	http.HandleFunc("/", handle)
	log.Fatalln(http.ListenAndServe(flagAddr, nil))
}
