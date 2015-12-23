package main

import (
	"github.com/boltdb/bolt"
	"gitlab.com/xojoc/util"
)

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
