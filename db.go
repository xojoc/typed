package main

import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
	"log"
	"os"
)

var DB *sql.DB

const DBname string = "articles.db"

func fileexists(n string) bool {
	_, err := os.Stat(n)
	if os.IsNotExist(err) {
		return false
	}
	return true
}

func init() {
	exists := fileexists(DBname)
	var err error
	DB, err = sql.Open("sqlite3", DBname)
	if err != nil {
		log.Fatal(err)
	}

	if !exists {
		articles := `create table Articles
(ID integer not null,
 Password text not null,
 Salt string not null,
 Markdown string not null,
 Gziped boolean not null,
 Etag integer not null,
 primary key(ID));`

		_, err = DB.Exec(articles)
		if err != nil {
			os.Remove(DBname)
			log.Fatal(err)
		}
	}

	err = DB.Ping()
	if err != nil {
		log.Fatal(err)
	}
}
