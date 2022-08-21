package store

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/db"
	dbutil "github.com/cosmos/cosmos-sdk/internal/db"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/rootmulti"
	"github.com/cosmos/cosmos-sdk/store/types"
	storev2types "github.com/cosmos/cosmos-sdk/store/v2alpha1"
	storev2 "github.com/cosmos/cosmos-sdk/store/v2alpha1/multi"
	"github.com/cosmos/cosmos-sdk/store/v2alpha1/smt"
)

// Fixed key length due to use of sha256
const keyLength = 32

var (
	dbBackends = []db.BackendType{db.MemDBBackend}
	// Reload store store after filling test data - state is not cached
	reloadStore = false
	// number of values of pre-filled data
	nValues = 200_000
	// number of ops per bench iteration
	totalOpsCount = 1000
	// how many operations in between commits
	opsPerCommit = 10000
)

func BenchmarkKVStore(b *testing.B) {
	v1_BenchmarkKVStore(b)
}
func BenchmarkMultiStore(b *testing.B) {
	v1_BenchmarkMultiStore(b)
}

func v1_BenchmarkKVStore(b *testing.B) {
	for _, dbt := range dbBackends {
		runRW(b, newIavlStore, dbt)
		runCommit(b, newIavlStore, dbt)
	}
}
func v1_BenchmarkMultiStore(b *testing.B) {
	for _, dbt := range dbBackends {
		runGetHistorical(b, newMultiV1, dbt)
		f := func(b *testing.B, dbc db.DBConnection, _ *types.CommitID) store { return newMultiV1(b, dbc) }
		runRW(b, f, dbt)
	}
}

func v2_BenchmarkKVStore(b *testing.B) {
	for _, dbt := range dbBackends {
		runRW(b, newSmtStore, dbt)
		runCommit(b, newSmtStore, dbt)
	}
}
func v2_BenchmarkMultiStore(b *testing.B) {
	for _, dbt := range dbBackends {
		runGetHistorical(b, newMultiV2, dbt)
		f := func(b *testing.B, dbc db.DBConnection, _ *types.CommitID) store { return newMultiV2(b, dbc) }
		runRW(b, f, dbt)
	}
}

type storeCtor = func(*testing.B, db.DBConnection, *types.CommitID) store
type versionedStoreCtor = func(*testing.B, db.DBConnection) versionedStore

type operation int

const (
	has operation = iota
	get
	insert
	update
	delete
	// prove
)

type ratios struct{ has, get, insert, update, delete int }

type smtStore struct {
	*smt.Store
	db db.DBConnection
	rw db.DBReadWriter
}

type versionedStore interface {
	store
	LoadVersion(int64)
}

type multistoreV1 struct {
	*rootmulti.Store
	types.KVStore
}

type multistoreV2 struct {
	*storev2.Store
	storev2types.KVStore
}

func (s *multistoreV1) LoadVersion(v int64) {
	s.Store.LoadVersion(v)
}

func (s *multistoreV2) LoadVersion(v int64) {
	vs, err := s.GetVersion(v)
	if err != nil {
		panic(err)
	}
	s.KVStore = vs.GetKVStore(skey_1)
}

func (s *smtStore) Commit() types.CommitID {
	if err := s.Store.Commit(); err != nil {
		panic(err)
	}
	if err := s.rw.Commit(); err != nil {
		panic(err)
	}
	root := s.Root()
	rw := s.db.ReadWriter()
	s.Store = smt.LoadStore(smt.StoreParams{TreeData: rw}, root)
	s.rw = rw
	return types.CommitID{Hash: root}
}

func newSmtStore(b *testing.B, dbc db.DBConnection, cid *types.CommitID) store {
	rw := dbc.ReadWriter()
	var s *smt.Store
	if cid == nil {
		s = smt.NewStore(smt.StoreParams{TreeData: rw})
	} else {
		s = smt.LoadStore(smt.StoreParams{TreeData: rw}, cid.Hash)
	}
	return &smtStore{s, dbc, rw}
}

func newIavlStore(b *testing.B, dbc db.DBConnection, cidp *types.CommitID) store {
	var cid types.CommitID
	if cidp != nil {
		cid = *cidp
	}
	s, err := iavl.LoadStore(dbutil.ConnectionAsTmdb(dbc), cid, false, iavlCacheSize)
	if err != nil {
		panic(err)
	}
	return s
}

func newMultiV1(b *testing.B, dbc db.DBConnection) versionedStore {
	store := rootmulti.NewStore(dbutil.ConnectionAsTmdb(dbc))
	store.MountStoreWithDB(skey_1, types.StoreTypeIAVL, nil)
	require.NoError(b, store.LoadLatestVersion())
	sub := store.GetKVStore(skey_1)
	return &multistoreV1{store, sub}
}

func newMultiV2(b *testing.B, dbc db.DBConnection) versionedStore {
	simpleStoreParams, err := simpleStoreParams()
	require.NoError(b, err)
	root, err := storev2.NewStore(dbc, simpleStoreParams)
	require.NoError(b, err)
	store := root.GetKVStore(skey_1)
	return &multistoreV2{root, store}
}

// creates a unique key for int x
func createDistinctKey(x int) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	wrote := binary.PutVarint(buf, int64(x))
	sum := sha256.Sum256(buf[:wrote])
	return sum[:]
}

func distinctKeys(from, to int) (ret [][]byte) {
	for i := from; i < to; i++ {
		ret = append(ret, createDistinctKey(i))
	}
	return
}

func sampleByRatio(r ratios) operation {
	ops := []operation{has, get, insert, update, delete}
	thresholds := []int{
		r.has,
		r.has + r.get,
		r.has + r.get + r.insert,
		r.has + r.get + r.insert + r.update,
	}
	total := r.has + r.get + r.insert + r.update + r.delete
	x := rand.Intn(total)
	for i := 0; i < len(thresholds); i++ {
		if x < thresholds[i] {
			return ops[i]
		}
	}
	return ops[len(ops)-1]
}

// runs Get and Set ops for all-present and all-absent keys
func runRW(b *testing.B, sctor storeCtor, dbt db.BackendType) {
	dir := b.TempDir()
	rand.Seed(seed)

	b.Run(fmt.Sprintf("%s", dbt), func(b *testing.B) {
		db, err := db.NewDB(string(dbt), dbt, dir)
		require.NoError(b, err)
		store := sctor(b, db, nil)
		values := prepareValues(nValues)
		keys := distinctKeys(0, nValues)
		nonkeys := distinctKeys(nValues, nValues*2)
		for i, v := range values {
			store.Set(keys[i], v)
		}
		var cid types.CommitID
		if reloadStore {
			cid = store.Commit()
		}
		reload := func() {
			if !reloadStore {
				return
			}
			if ms, ok := store.(*multistoreV2); ok {
				if err := ms.Close(); err != nil {
					panic(err)
				}
			}
			store = sctor(b, db, &cid)
		}

		reload()
		b.Run("get-present", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < totalOpsCount; j++ {
					ki := rand.Intn(nValues)
					store.Get(keys[ki])
				}
			}
		})

		reload()
		b.Run("get-absent", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < totalOpsCount; j++ {
					ki := rand.Intn(nValues)
					store.Get(nonkeys[ki])
				}
			}
		})

		reload()
		b.Run("update", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < totalOpsCount; j++ {
					ki := rand.Intn(nValues)
					store.Set(keys[ki], values[len(values)-1-ki])
				}
			}
		})

		reload()
		b.Run("insert", func(b *testing.B) {
			if b.N*totalOpsCount >= len(nonkeys) {
				nonkeys = distinctKeys(nValues, nValues+2*len(nonkeys))
				b.ResetTimer()
			}
			for i := 0; i < b.N; i++ {
				for j := 0; j < totalOpsCount; j++ {
					ki := i*totalOpsCount + j
					store.Set(nonkeys[ki], values[ki%len(values)])
				}
			}
		})

		reload()
		b.Run("delete", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < totalOpsCount; j++ {
					ki := rand.Intn(nValues)
					store.Delete(keys[ki])
				}
			}
		})
		db.Close()
	})
}

// test historical version access (read-only test cases)
func runGetHistorical(b *testing.B, sctor versionedStoreCtor, dbt db.BackendType) {
	dir := b.TempDir()
	rand.Seed(seed)

	name := fmt.Sprintf("%s/get-hist", dbt)
	values := prepareValues(nValues)
	b.Run(name, func(b *testing.B) {
		db, err := db.NewDB(name, dbt, dir)
		require.NoError(b, err)
		store := sctor(b, db)
		var lastversion int64
		for i, v := range values {
			store.Set(createDistinctKey(i*2), v) // half of read keys will be present
			if i%opsPerCommit == 0 {
				cid := store.Commit()
				lastversion = cid.Version
			}
		}
		if lastversion < 2 {
			b.Fatal("historical version requires > 1 commits")
		}
		store.LoadVersion(lastversion - 1)
		readKeys := distinctKeys(0, nValues)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for j := 0; j < totalOpsCount; j++ {
				ki := rand.Intn(nValues)
				store.Get(readKeys[ki])
			}
		}
		b.StopTimer()
		db.Close()
	})
}

func runCommit(b *testing.B, sctor storeCtor, dbt db.BackendType) {
	dir := b.TempDir()
	rand.Seed(seed)

	rats := ratios{get: 50, insert: 20, update: 25, delete: 5}

	b.Run(fmt.Sprintf("%s/commit", dbt), func(b *testing.B) {
		db, err := db.NewDB(string(dbt), dbt, dir)
		require.NoError(b, err)
		store := sctor(b, db, nil)
		values := prepareValues(nValues)
		keys := distinctKeys(0, nValues)
		deleted := map[int]bool{}
		for i, v := range values {
			store.Set(createDistinctKey(i), v)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for j := 0; j < opsPerCommit; j++ {
				op := sampleByRatio(rats)
				if len(keys) < 2*len(deleted) {
					op = insert
				}
				vi := rand.Intn(len(values))
				if op == insert {
					key := createDistinctKey(len(keys))
					store.Set(key, values[vi])
					keys = append(keys, key)
					continue
				}
				var ki int
				for { // find a present key
					ki = rand.Intn(len(keys))
					if !deleted[ki] {
						break
					}
				}
				switch op {
				case has:
					store.Has(keys[ki])
				case get:
					store.Get(keys[ki])
				case update:
					store.Set(keys[ki], values[vi])
				case delete:
					store.Delete(keys[ki])
					deleted[ki] = true
				}
			}
			b.StartTimer()
			_ = store.Commit()
			b.StopTimer()
		}
		db.Close()
	})
}
