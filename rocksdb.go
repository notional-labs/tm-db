//go:build rocksdb
// +build rocksdb

package db

import (
	"fmt"
	"github.com/tecbot/gorocksdb"
	"gopkg.in/ini.v1"
	"path/filepath"
	"runtime"
	"strings"
)

func init() {
	dbCreator := func(name string, dir string) (DB, error) {
		return NewRocksDB(name, dir)
	}
	registerDBCreator(RocksDBBackend, dbCreator, false)
}

// RocksDB is a RocksDB backend.
type RocksDB struct {
	db     *gorocksdb.DB
	ro     *gorocksdb.ReadOptions
	wo     *gorocksdb.WriteOptions
	woSync *gorocksdb.WriteOptions
}

var _ DB = (*RocksDB)(nil)

func NewRocksDB(name string, dir string) (*RocksDB, error) {
	// default rocksdb option, good enough for most cases, including heavy workloads.
	// 1GB table cache, 512MB write buffer(may use 50% more on heavy workloads).
	// compression: snappy as default, need to -lsnappy to enable.
	bbto := gorocksdb.NewDefaultBlockBasedTableOptions()
	bbto.SetBlockCache(gorocksdb.NewLRUCache(1 << 30))
	bbto.SetFilterPolicy(gorocksdb.NewBloomFilter(10))

	base_opts := gorocksdb.NewDefaultOptions()
	base_opts.SetBlockBasedTableFactory(bbto)
	base_opts.SetCreateIfMissing(true)
	base_opts.IncreaseParallelism(runtime.NumCPU())
	// 1.5GB maximum memory use for writebuffer.
	base_opts.OptimizeLevelStyleCompaction(512 * 1024 * 1024)

	// Options file have to be the same dir and the same name as the db with .ini extension
	// eg., db path = /a/b/c.db => options file = /a/b/c.db.ini
	opts_file := dir + "/" + name + ".ini"
	if FileExists(opts_file) {
		cfg, err := ini.Load(opts_file)
		if err != nil {
			return nil, err
		}

		// GetOptionsFromString supports DBOptions and CFOptions only
		dbopts := cfg.Section("DBOptions")
		cfopts := cfg.Section("CFOptions \"default\"")

		lines := []string{}
		for k, v := range dbopts.KeysHash() {
			str_pair := fmt.Sprintf("%s=%s", k, v)
			lines = append(lines, str_pair)
		}
		for k, v := range cfopts.KeysHash() {
			str_pair := fmt.Sprintf("%s=%s", k, v)
			lines = append(lines, str_pair)
		}

		opts_str := strings.Join(lines, ";")
		opts, err := gorocksdb.GetOptionsFromString(base_opts, opts_str)
		if err != nil {
			return nil, err
		}

		// there is "TableOptions/BlockBasedTable \"default\"" section, could also be used for bbto.

		return NewRocksDBWithOptions(name, dir, opts)
	}

	// options file does *not* exist => using default options
	return NewRocksDBWithOptions(name, dir, base_opts)
}

func NewRocksDBWithOptions(name string, dir string, opts *gorocksdb.Options) (*RocksDB, error) {
	dbPath := filepath.Join(dir, name+".db")
	db, err := gorocksdb.OpenDb(opts, dbPath)
	if err != nil {
		return nil, err
	}
	ro := gorocksdb.NewDefaultReadOptions()
	wo := gorocksdb.NewDefaultWriteOptions()
	woSync := gorocksdb.NewDefaultWriteOptions()
	woSync.SetSync(true)
	database := &RocksDB{
		db:     db,
		ro:     ro,
		wo:     wo,
		woSync: woSync,
	}
	return database, nil
}

// Get implements DB.
func (db *RocksDB) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, errKeyEmpty
	}
	res, err := db.db.Get(db.ro, key)
	if err != nil {
		return nil, err
	}
	return moveSliceToBytes(res), nil
}

// Has implements DB.
func (db *RocksDB) Has(key []byte) (bool, error) {
	bytes, err := db.Get(key)
	if err != nil {
		return false, err
	}
	return bytes != nil, nil
}

// Set implements DB.
func (db *RocksDB) Set(key []byte, value []byte) error {
	if len(key) == 0 {
		return errKeyEmpty
	}
	if value == nil {
		return errValueNil
	}
	err := db.db.Put(db.wo, key, value)
	if err != nil {
		return err
	}
	return nil
}

// SetSync implements DB.
func (db *RocksDB) SetSync(key []byte, value []byte) error {
	if len(key) == 0 {
		return errKeyEmpty
	}
	if value == nil {
		return errValueNil
	}
	err := db.db.Put(db.woSync, key, value)
	if err != nil {
		return err
	}
	return nil
}

// Delete implements DB.
func (db *RocksDB) Delete(key []byte) error {
	if len(key) == 0 {
		return errKeyEmpty
	}
	err := db.db.Delete(db.wo, key)
	if err != nil {
		return err
	}
	return nil
}

// DeleteSync implements DB.
func (db *RocksDB) DeleteSync(key []byte) error {
	if len(key) == 0 {
		return errKeyEmpty
	}
	err := db.db.Delete(db.woSync, key)
	if err != nil {
		return nil
	}
	return nil
}

func (db *RocksDB) DB() *gorocksdb.DB {
	return db.db
}

// Close implements DB.
func (db *RocksDB) Close() error {
	db.ro.Destroy()
	db.wo.Destroy()
	db.woSync.Destroy()
	db.db.Close()
	return nil
}

// Print implements DB.
func (db *RocksDB) Print() error {
	itr, err := db.Iterator(nil, nil)
	if err != nil {
		return err
	}
	defer itr.Close()
	for ; itr.Valid(); itr.Next() {
		key := itr.Key()
		value := itr.Value()
		fmt.Printf("[%X]:\t[%X]\n", key, value)
	}
	return nil
}

// Stats implements DB.
func (db *RocksDB) Stats() map[string]string {
	keys := []string{"rocksdb.stats"}
	stats := make(map[string]string, len(keys))
	for _, key := range keys {
		stats[key] = db.db.GetProperty(key)
	}
	return stats
}

// NewBatch implements DB.
func (db *RocksDB) NewBatch() Batch {
	return newRocksDBBatch(db)
}

// Iterator implements DB.
func (db *RocksDB) Iterator(start, end []byte) (Iterator, error) {
	if (start != nil && len(start) == 0) || (end != nil && len(end) == 0) {
		return nil, errKeyEmpty
	}
	itr := db.db.NewIterator(db.ro)
	return newRocksDBIterator(itr, start, end, false), nil
}

// ReverseIterator implements DB.
func (db *RocksDB) ReverseIterator(start, end []byte) (Iterator, error) {
	if (start != nil && len(start) == 0) || (end != nil && len(end) == 0) {
		return nil, errKeyEmpty
	}
	itr := db.db.NewIterator(db.ro)
	return newRocksDBIterator(itr, start, end, true), nil
}
