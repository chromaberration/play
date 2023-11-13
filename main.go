package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
)

//go:embed index.html
var html []byte

type Cache struct {
	sync.Mutex
	runtimes map[string]Runtime
}

var cache Cache

func init() {
	cache.runtimes = make(map[string]Runtime)
}

type Runtime struct {
	Script string
	Binary string
}

func readZipFile(file *zip.File) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}

func removeRootDirFromZip(zipData []byte) ([]byte, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, err
	}

	var modifiedZipBuffer bytes.Buffer
	zipWriter := zip.NewWriter(&modifiedZipBuffer)

	for _, file := range zipReader.File {
		file.Name = strings.Join(strings.Split(file.Name, "/")[1:], "/")

		destFile, err := zipWriter.Create(file.Name)
		if err != nil {
			return nil, err
		}

		srcFile, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer srcFile.Close()

		_, err = io.Copy(destFile, srcFile)
		if err != nil {
			return nil, err
		}
	}

	err = zipWriter.Close()
	if err != nil {
		return nil, err
	}

	return modifiedZipBuffer.Bytes(), nil
}

func fetchRuntime(runtime string) (Runtime, error) {
	cache.Lock()
	defer cache.Unlock()

	if cached, ok := cache.runtimes[runtime]; ok {
		return cached, nil
	}

	url := fmt.Sprintf("https://github.com/carimbolabs/carimbo/releases/download/v%s/WebAssembly.zip", runtime)

	resp, err := http.Get(url)
	if err != nil {
		return Runtime{}, fmt.Errorf("[http.Get] error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Runtime{}, fmt.Errorf("[io.ReadAll]: error %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return Runtime{}, fmt.Errorf("[zip.NewReader]: error %v", err)
	}

	var scriptContent, binaryContent []byte
	for _, file := range zr.File {
		switch file.Name {
		case "carimbo.js":
			scriptContent, err = readZipFile(file)
			if err != nil {
				return Runtime{}, fmt.Errorf("[readZipFile]: error %v", err)
			}
		case "carimbo.wasm":
			binaryContent, err = readZipFile(file)
			if err != nil {
				return Runtime{}, fmt.Errorf("[readZipFile]: error %v", err)
			}
		}
	}

	r := Runtime{Script: string(scriptContent), Binary: string(binaryContent)}
	cache.runtimes[runtime] = r
	return r, nil
}

func fetchBundle(org, repo, release string) ([]byte, error) {
	url := fmt.Sprintf("https://github.com/%s/%s/archive/refs/tags/v%s.zip", org, repo, release)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("[http.Get] error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("[io.ReadAll]: error %v", err)
	}

	body, err = removeRootDirFromZip(body)
	if err != nil {
		return nil, fmt.Errorf("[removeRootDirFromZip]: error %v", err)
	}

	return body, nil
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, contentType string, data []byte) {
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}

func jsHandler(w http.ResponseWriter, r *http.Request) {
	runtime, err := fetchRuntime(getRuntimeFromURL(r.URL.Path))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	serveStaticFile(w, r, "application/javascript", []byte(runtime.Script))
}

func wasmHandler(w http.ResponseWriter, r *http.Request) {
	runtime, err := fetchRuntime(getRuntimeFromURL(r.URL.Path))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	serveStaticFile(w, r, "application/wasm", []byte(runtime.Binary))
}

func zipHandler(w http.ResponseWriter, r *http.Request) {
	_, org, repo, release := getOrgRepoReleaseFromURL(r.URL.Path)
	bundle, err := fetchBundle(org, repo, release)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	serveStaticFile(w, r, "application/zip", bundle)
}

func getOrgRepoReleaseFromURL(urlPath string) (string, string, string, string) {
	pattern := regexp.MustCompile(`/(?P<runtime>[^/]+)/(?P<org>[^/]+)/(?P<repo>[^/]+)/(?P<release>[^/]+)`)
	match := pattern.FindStringSubmatch(urlPath)

	var runtime, org, repo, release string
	for i, name := range pattern.SubexpNames() {
		if i != 0 && name != "" {
			switch name {
			case "runtime":
				runtime = match[i]
			case "org":
				org = match[i]
			case "repo":
				repo = match[i]
			case "release":
				release = match[i]
			}
		}
	}

	return runtime, org, repo, release
}

func getRuntimeFromURL(urlPath string) string {
	runtime, _, _, _ := getOrgRepoReleaseFromURL(urlPath)
	return runtime
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.Header().Set("Content-Type", "image/x-icon")
	w.Write([]byte{})
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write(html)
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".js") {
			jsHandler(w, r)
		} else if strings.HasSuffix(r.URL.Path, ".wasm") {
			wasmHandler(w, r)
		} else if strings.HasSuffix(r.URL.Path, ".zip") {
			zipHandler(w, r)
		} else if strings.HasSuffix(r.URL.Path, ".ico") {
			faviconHandler(w, r)
		} else {
			rootHandler(w, r)
		}
	})

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", os.Getenv("PORT")), nil))
}
