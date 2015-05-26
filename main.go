package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"database/sql"
	"fmt"
	"github.com/facebookgo/grace/gracehttp"
	"github.com/twinj/uuid"
	htpl "html/template"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
)

const (
	gzipThreshold = 200
)

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
	a := &Article{}
	err := DB.QueryRow(`select Password, Salt, Markdown, Gziped, ETag from articles where ID=?;`, id).Scan(&a.Password, &a.Salt, &a.Markdown, &a.Gziped, &a.ETag)
	if err != nil {
		return nil, err
	}
	if a.Gziped {
		gz, err := gzip.NewReader(strings.NewReader(a.Markdown))
		if err != nil {
			return nil, err
		}
		b, err := ioutil.ReadAll(gz)
		if err != nil {
			return nil, err
		}
		err = gz.Close()
		if err != nil {
			return nil, err
		}
		a.Markdown = string(b)
		a.Gziped = false
	}
	a.ID = id
	return a, err
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
	var err error
	c := exec.Command("pandoc", "-t", "html5")
	r := strings.NewReader(a.Markdown)
	c.Stdin = r
	h, err := c.Output()
	if err != nil {
		return htpl.HTML(""), err
	}
	return htpl.HTML(h), nil
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
	case p == "/index.html" || p == "/":
		http.Redirect(w, r, "http://typed.pw", http.StatusMovedPermanently)
		return nil
	case p == "":
		err := templates.ExecuteTemplate(w, "index.html", nil)
		if err != nil {
			return &NetError{500, err.Error()}
		}
		return nil
	default: /* Static files */
		w.Header().Add("Cache-Control", "max-age=604800, public")
		http.ServeFile(w, r, "."+p)
		return nil
	}
	return nil
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
		if err == sql.ErrNoRows {
			return &NetError{404, err.Error()}
		} else {
			return &NetError{500, err.Error()}
		}
	}
	w.Header().Add("Cache-Control", "no-cache")
	w.Header().Add("ETag", fmt.Sprint(a.ETag))
	if r.Header.Get("If-None-Match") == fmt.Sprint(a.ETag) {
		return &NetError{304, ""}
	}
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
		res, err := DB.Exec(`INSERT INTO articles (Password, Salt, Markdown, Gziped, ETag) VALUES(?,?,?,?,0);`, p, s, m, g)
		if err != nil {
			return &NetError{500, err.Error()}
		}
		id, err := res.LastInsertId()
		if err != nil {
			return &NetError{500, err.Error()}
		}
		a := &Article{ID: uint64(id)}
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
			if err == sql.ErrNoRows {
				return &NetError{404, err.Error()}
			} else {
				return &NetError{500, err.Error()}
			}
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
			fmt.Print("401")
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
		_, err = DB.Exec(`update articles set Markdown=?, Gziped=?, ETag=? where id=?;`, m, g, a.ETag+1, a.ID)
		if err != nil {
			return &NetError{500, err.Error()}
		}
		http.Redirect(w, r, a.AbsPath(), http.StatusSeeOther)
	} else {
		return &NetError{500, "can't handle verb"}
	}
	return nil
}

func staticHandler(w http.ResponseWriter, r *http.Request) *NetError {
	f, err := os.Open(r.URL.Path)
	if err != nil {
		return &NetError{404, err.Error()}
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(r.URL.Path)))
	_, err = io.Copy(w, f)
	if err != nil {
		return &NetError{500, err.Error()}
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
	//	http.Handle("/html/", http.FileServer(http.Dir(".")))
	//	http.Handle("/md/", http.FileServer(http.Dir(".")))
	gracehttp.Serve(&http.Server{Addr: p})
}
