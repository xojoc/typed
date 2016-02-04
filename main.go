// Written by http://xojoc.pw. Public Domain.

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/gob"
	"errors"
	"fmt"
	htpl "html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/boltdb/bolt"
	"github.com/dustin/go-humanize"
	"github.com/facebookgo/grace/gracehttp"
	"github.com/golang-commonmark/markdown"
	"github.com/twinj/uuid"
	"gitlab.com/xojoc/util"
)

const (
	gzipThreshold = 200
	postLimit     = 30000
)

var notFound = errors.New("not found")

var boltdb *bolt.DB

func init() {
	var err error
	boltdb, err = bolt.Open("articles.bolt", 0600, nil)
	util.Fatal(err)

	boltdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("articles"))
		util.Fatal(err)
		return nil
	})
}

func init() {
	log.SetFlags(log.Lshortfile)
	f, err := os.OpenFile("log.txt", os.O_RDWR|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		log.Print(err)
	} else {
		log.SetOutput(f)
	}
}

var templates = htpl.Must(htpl.New("").Funcs(htpl.FuncMap{}).ParseGlob("*.html"))

type Article struct {
	ID       uint64
	Password string
	Salt     string
	Markdown string
	Gziped   bool
	ETag     uint64
}

type NetError struct {
	Code    int
	Message string
}

func (a *Article) AbsPath() string {
	return "/a/" + fmt.Sprint(a.ID)
}
func (a *Article) EditPath() string {
	return "/edit/" + fmt.Sprint(a.ID)
}

func getArticleByID(id uint64) (*Article, error) {
	var a Article
	return &a, boltdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("articles"))
		v := b.Get([]byte(fmt.Sprint(id)))
		if v == nil {
			return notFound
		}
		dec := gob.NewDecoder(bytes.NewBuffer(v))
		err := dec.Decode(&a)
		if err != nil {
			return err
		}
		if a.Gziped {
			gz, err := gzip.NewReader(strings.NewReader(a.Markdown))
			if err != nil {
				return err
			}
			b, err := ioutil.ReadAll(gz)
			if err != nil {
				return err
			}
			err = gz.Close()
			if err != nil {
				return err
			}
			a.Markdown = string(b)
			a.Gziped = false
		}
		a.ID = id
		return nil
	})
}

func (a *Article) Title() string {
	z := strings.Split(a.Markdown, "\n")
	for _, s := range z {
		if strings.HasPrefix(s, "#") {
			return strings.TrimLeft(s, `# `)
		}
	}
	return fmt.Sprint(a.ID)
}

func (a *Article) ToHTML() (htpl.HTML, error) {
	md := markdown.New()
	return htpl.HTML(md.RenderToString([]byte(a.Markdown))), nil
}

type myHandler func(http.ResponseWriter, *http.Request) *NetError

func hashPassword(p string, salt string) string {
	if p == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha512.Sum512([]byte(p+salt)))
}

func errorHandler(h myHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nerr := h(w, r)
		if nerr != nil {
			if nerr.Code == 404 {
				log.Printf("Path %q not found: %s", r.URL.Path, nerr.Message)
				w.WriteHeader(404)
				err := templates.ExecuteTemplate(w, "404.html", nil)
				if err != nil {
					http.NotFound(w, r)
				}
			} else if nerr.Code == 401 {
				w.WriteHeader(401)
				fmt.Fprint(w, "Wrong password, please go back and try again.")
			} else if nerr.Code == 304 {
				w.WriteHeader(304)
			} else {
				log.Printf("Path %q error: %s", r.URL.Path, nerr.Message)
				w.WriteHeader(500)
				err := templates.ExecuteTemplate(w, "500.html", nerr.Message)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}
		}
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) *NetError {
	p := r.URL.Path
	switch {
	case p == "/index.html" || p == "":
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
		return nil
	case p == "/":
		n := 0
		err := boltdb.View(func(tx *bolt.Tx) error {
			n = tx.Bucket([]byte("articles")).Stats().KeyN
			return nil
		})
		if err != nil {
			return &NetError{500, err.Error()}
		}
		err = templates.ExecuteTemplate(w, "index.html", humanize.Comma(int64(n)))
		if err != nil {
			return &NetError{500, err.Error()}
		}
		return nil
	case p == "/main.css" || p == "/favicon.ico":
		w.Header().Add("Cache-Control", "max-age=604800, public")
		http.ServeFile(w, r, "."+p)
		return nil
	default:
		return &NetError{404, "not found"}
	}
	return nil
}

func isCached(r *http.Request, a *Article) bool {
	for _, s := range r.Header["Cache-Control"] {
		if s == "max-age=0" {
			return false
		}
	}
	return strings.TrimPrefix(r.Header.Get("If-None-Match"), "W/") == fmt.Sprintf(`"%d"`, a.ETag)
}

func aHandler(w http.ResponseWriter, r *http.Request) *NetError {
	idstr := r.URL.Path[len("/a/"):]
	if idstr == "" {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
		return nil
	}
	id, err := strconv.ParseUint(idstr, 10, 64)
	if err != nil {
		return &NetError{404, err.Error()}
	}
	a, err := getArticleByID(id)
	if err != nil {
		if err == notFound {
			return &NetError{404, err.Error()}
		} else {
			return &NetError{500, err.Error()}
		}
	}
	w.Header().Add("Cache-Control", "public, max-age=3600") // one hour
	//	w.Header().Add("ETag", fmt.Sprintf(`"%d"`, a.ETag))
	//	if isCached(r, a) {
	//		return &NetError{304, ""}
	//	}
	err = templates.ExecuteTemplate(w, "a.html", a)
	if err != nil {
		return &NetError{500, err.Error()}
	}
	return nil
}

func newHandler(w http.ResponseWriter, r *http.Request) *NetError {
	if r.Method == "GET" {
		err := templates.ExecuteTemplate(w, "form.html", nil)
		if err != nil {
			return &NetError{500, err.Error()}
		}
		return nil
	} else if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, postLimit)
		r.ParseForm()
		p := ""
		s := ""
		if r.PostForm.Get("newpassword") != "" {
			s = uuid.NewV4().String()
			p = hashPassword(r.PostForm.Get("newpassword"), s)
		}
		g := false
		m := r.PostForm.Get("newbody")
		if len(m) >= gzipThreshold {
			g = true
			var b bytes.Buffer
			gz, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
			_, err := io.WriteString(gz, m)
			if err != nil {
				return &NetError{500, err.Error()}
			}
			err = gz.Close()
			if err != nil {
				return &NetError{500, err.Error()}
			}
			m = b.String()
		}
		var a Article
		a.Password = p
		a.Salt = s
		a.Markdown = m
		a.Gziped = g
		a.ETag = 0
		err := boltdb.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("articles"))
			a.ID, _ = b.NextSequence()
			var buf bytes.Buffer
			enc := gob.NewEncoder(&buf)
			err := enc.Encode(&a)
			if err != nil {
				return err
			}
			return b.Put([]byte(fmt.Sprint(a.ID)), buf.Bytes())
		})
		if err != nil {
			return &NetError{500, err.Error()}
		}
		http.Redirect(w, r, a.AbsPath(), http.StatusSeeOther)
	} else {
		return &NetError{500, "can't handle verb"}
	}
	return nil
}

func editHandler(w http.ResponseWriter, r *http.Request) *NetError {
	if r.Method == "GET" {
		idstr := r.URL.Path[len("/edit/"):]
		if idstr == "" {
			http.Redirect(w, r, "/", http.StatusMovedPermanently)
			return nil
		}
		id, err := strconv.ParseUint(idstr, 10, 64)
		if err != nil {
			return &NetError{404, err.Error()}
		}
		a, err := getArticleByID(id)
		if err != nil {
			if err == notFound {
				return &NetError{404, err.Error()}
			} else {
				return &NetError{500, err.Error()}
			}
		}
		w.Header().Add("Cache-Control", "public, no-cache")
		w.Header().Add("ETag", fmt.Sprintf(`"%d"`, a.ETag))
		if isCached(r, a) {
			return &NetError{304, ""}
		}
		err = templates.ExecuteTemplate(w, "form.html", a)
		if err != nil {
			return &NetError{500, err.Error()}
		}
		return nil
	} else if r.Method == "POST" {
		idstr := r.URL.Path[len("/edit/"):]
		if idstr == "" {
			http.Redirect(w, r, "/", http.StatusMovedPermanently)
			return nil
		}
		id, err := strconv.ParseUint(idstr, 10, 64)
		if err != nil {
			return &NetError{500, err.Error()}
		}
		a, err := getArticleByID(id)
		if err != nil {
			return &NetError{500, err.Error()}
		}

		r.ParseForm()
		if a.Password == "" || hashPassword(r.PostForm.Get("newpassword"), a.Salt) != a.Password {
			return &NetError{401, "wrong password"}
		}
		g := false
		m := r.PostForm.Get("newbody")
		if len(m) >= gzipThreshold {
			g = true
			var b bytes.Buffer
			gz, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
			_, err := io.WriteString(gz, m)
			if err != nil {
				return &NetError{500, err.Error()}
			}
			err = gz.Close()
			if err != nil {
				return &NetError{500, err.Error()}
			}
			m = b.String()
		}
		a.Markdown = m
		a.Gziped = g
		a.ETag += 1
		err = boltdb.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("articles"))
			var buf bytes.Buffer
			enc := gob.NewEncoder(&buf)
			err := enc.Encode(&a)
			if err != nil {
				return err
			}
			return b.Put([]byte(fmt.Sprint(a.ID)), buf.Bytes())
		})
		if err != nil {
			return &NetError{500, err.Error()}
		}
		http.Redirect(w, r, a.AbsPath()+"?etag="+fmt.Sprint(a.ETag), http.StatusSeeOther)
	} else {
		return &NetError{500, "can't handle verb"}
	}
	return nil
}

func main() {
	p := ":4446"
	if len(os.Args) > 1 {
		p = os.Args[1]
	}
	http.HandleFunc("/", errorHandler(rootHandler))
	http.Handle("/new", errorHandler(newHandler))
	http.HandleFunc("/a/", errorHandler(aHandler))
	http.HandleFunc("/edit/", errorHandler(editHandler))
	gracehttp.Serve(&http.Server{Addr: p})
}
