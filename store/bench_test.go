package store

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/db"
	storev1 "github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
	storev2 "github.com/cosmos/cosmos-sdk/store/v2alpha1/multi"
	tmdb "github.com/tendermint/tm-db"
)

var (
	skey_1 = types.NewKVStoreKey("store1")
	seed   = int64(42)

	iavlCacheSize  = 10_000
	commitInterval = 200
)

func BenchmarkStoreCombined(b *testing.B) {
	v1_BenchmarkStoreCombined(b)
}

func v1_BenchmarkStoreCombined(b *testing.B) {
	dbBackendTypes := []tmdb.BackendType{tmdb.BadgerDBBackend}
	runSuite(b, 1, dbBackendTypes, b.TempDir())
}

func v2_BenchmarkStoreCombined(b *testing.B) {
	dbBackendTypes := []tmdb.BackendType{tmdb.BadgerDBBackend}
	runSuite(b, 2, dbBackendTypes, b.TempDir())
}

func randBytes(numBytes int) []byte {
	b := make([]byte, numBytes)
	_, _ = rand.Read(b)
	return b
}

type percentages struct {
	has    int
	get    int
	set    int
	delete int
}

type counts struct {
	has    int
	get    int
	set    int
	delete int
}

func generateGradedPercentages() []percentages {
	var sampledPercentages []percentages
	sampleX := percentages{has: 2, get: 55, set: 40, delete: 3}
	sampledPercentages = append(sampledPercentages, sampleX)
	for a := 0; a < 100; a += 20 {
		for b := 0; b <= 100-a; b += 20 {
			for c := 0; c < 100-a-b; c += 20 {
				sample := percentages{
					has:    a,
					get:    b,
					set:    c,
					delete: 100 - a - b - c,
				}
				sampledPercentages = append(sampledPercentages, sample)
			}
		}
	}
	return sampledPercentages
}

func generateExtremePercentages() []percentages {
	return []percentages{
		{100, 0, 0, 0},
		{0, 100, 0, 0},
		{0, 0, 100, 0},
		{0, 0, 0, 100},
	}
}

type benchmark struct {
	name        string
	percentages percentages
	dbType      tmdb.BackendType
	counts      counts
}

func generateBenchmarks(dbBackendTypes []tmdb.BackendType, sampledPercentages []percentages, sampledCounts []counts) []benchmark {
	var benchmarks []benchmark
	for _, dbType := range dbBackendTypes {
		if len(sampledPercentages) > 0 {
			for _, p := range sampledPercentages {
				name := fmt.Sprintf("%s-r-%d-%d-%d-%d", dbType, p.has, p.get, p.set, p.delete)
				benchmarks = append(benchmarks, benchmark{name: name, percentages: p, dbType: dbType, counts: counts{}})
			}
		} else if len(sampledCounts) > 0 {
			for _, c := range sampledCounts {
				name := fmt.Sprintf("%s-d-%d-%d-%d-%d", dbType, c.has, c.get, c.set, c.delete)
				benchmarks = append(benchmarks, benchmark{name: name, percentages: percentages{}, dbType: dbType, counts: c})
			}
		}
	}
	return benchmarks
}

type store interface {
	Has(key []byte) bool
	Get(key []byte) []byte
	Set(key []byte, value []byte)
	Delete(key []byte)
	Commit() types.CommitID
}

func simpleStoreParams() (storev2.StoreParams, error) {
	opts := storev2.DefaultStoreParams()
	err := opts.RegisterSubstore(skey_1, types.StoreTypePersistent)
	if err != nil {
		return storev2.StoreParams{}, err
	}
	return opts, nil
}

func sampleOperation(p percentages) string {
	ops := []string{"Has", "Get", "Set", "Delete"}
	thresholds := []int{p.has, p.has + p.get, p.has + p.get + p.set}
	r := rand.Intn(100)
	for i := 0; i < len(thresholds); i++ {
		if r < thresholds[i] {
			return ops[i]
		}
	}
	return ops[3]
}

func runRandomizedOperations(b *testing.B, s store, totalOpsCount int, p percentages) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < totalOpsCount; j++ {
			b.StopTimer()
			op := sampleOperation(p)
			b.StartTimer()

			switch op {
			case "Has":
				s.Has(randBytes(12))
			case "Get":
				s.Get(randBytes(12))
			case "Set":
				s.Set(randBytes(12), randBytes(50))
			case "Delete":
				s.Delete(randBytes(12))
			}
			if j%commitInterval == 0 || j == totalOpsCount-1 {
				s.Commit()
			}
		}
	}
}

func prepareValues(n int) [][]byte {
	var data [][]byte
	for i := 0; i < n; i++ {
		data = append(data, randBytes(50))
	}
	return data
}

func createSineKey(i int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(math.Sin(float64(i))*100000))
	return b
}

func runDeterministicOperations(b *testing.B, s store, values [][]byte, c counts) {
	counts := []int{c.has, c.get, c.set, c.delete}
	sort.Ints(counts)
	step := counts[len(counts)-1]
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		idx := i * step

		b.StopTimer()
		if idx >= len(values) {
			for j := len(values); j < (idx + step); j++ {
				values = append(values, randBytes(50))
			}
		}

		b.StartTimer()
		for j := 0; j < c.set; j++ {
			key := createSineKey(idx + j)
			s.Set(key, values[idx+j])
		}
		for j := 0; j < c.has; j++ {
			key := createSineKey(idx + j)
			s.Has(key)
		}
		for j := 0; j < c.get; j++ {
			key := createSineKey(idx + j)
			s.Get(key)
		}
		for j := 0; j < c.delete; j++ {
			key := createSineKey(idx + j)
			s.Delete(key)
		}
		s.Commit()
	}
}

func RunRvert(b *testing.B, s store, db interface{}, uncommittedValues [][]byte) store {
	for i := 0; i < b.N; i++ {
		// Key, value pairs changed but not committed
		for i, v := range uncommittedValues {
			s.Set(createSineKey(i), v)
		}

		b.ResetTimer()
		switch t := s.(type) {
		case *storev1.Store:
			_, err := newStore(1, db) // This shall revert to the last commitID
			require.NoError(b, err)
		case *multistoreV2:
			require.NoError(b, t.Close())
			var err error
			s, err = newStore(2, db) // This shall revert to the last commitID
			require.NoError(b, err)
		default:
			panic("not supported store type")
		}
	}
	return s
}

func newDB(version int, dbName string, dbType tmdb.BackendType, dir string) (interface{}, error) {
	d := filepath.Join(dir, dbName, dbName+".db")
	err := os.MkdirAll(d, os.ModePerm)
	if err != nil {
		panic(err)
	}

	if version == 1 {
		return tmdb.NewDB(dbName, dbType, d)
	}

	if version == 2 {
		return db.NewDB(dbName, db.BackendType(string(dbType)), d)
	}

	return nil, fmt.Errorf("not supported version")
}

func newStore(version int, dbBackend interface{}) (store, error) {
	if version == 1 {
		db, ok := dbBackend.(tmdb.DB)
		if !ok {
			return nil, fmt.Errorf("unsupported db type")
		}
		s, err := storev1.LoadStore(db, types.CommitID{Version: 0, Hash: nil}, false, iavlCacheSize)
		if err != nil {
			return nil, err
		}
		return s, nil
	}

	if version == 2 {
		db, ok := dbBackend.(db.DBConnection)
		if !ok {
			return nil, fmt.Errorf("unsupported db type")
		}
		simpleStoreParams, err := simpleStoreParams()
		if err != nil {
			return nil, err
		}
		root, err := storev2.NewStore(db, simpleStoreParams)
		if err != nil {
			return nil, err
		}
		store := root.GetKVStore(skey_1)
		s := &multistoreV2{root, store}
		return s, nil
	}

	return nil, fmt.Errorf("unsupported version")
}

func prepareStore(b *testing.B, version int, dbType tmdb.BackendType, committedValues [][]byte) (store, interface{}) {
	dir := fmt.Sprintf("testdbs/v%d", version)
	dbName := fmt.Sprintf("reverttest-%s", dbType)
	db, err := newDB(version, dbName, dbType, dir)
	require.NoError(b, err)
	s, err := newStore(version, db)
	require.NoError(b, err)
	for i, v := range committedValues {
		s.Set(createSineKey(i), v)
	}
	_ = s.Commit()

	return s, db
}

func runSuite(b *testing.B, version int, dbBackendTypes []tmdb.BackendType, dir string) {
	// run randomized operations subbenchmarks for various scenarios
	sampledPercentages := generateGradedPercentages()
	benchmarks := generateBenchmarks(dbBackendTypes, sampledPercentages, nil)

	values := prepareValues(5000)
	for _, bm := range benchmarks {
		db, err := newDB(version, bm.name, bm.dbType, dir)
		require.NoError(b, err)
		s, err := newStore(version, db)
		require.NoError(b, err)
		// add existing data
		for i, v := range values {
			s.Set(createSineKey(i), v)
		}
		b.Run(bm.name, func(sub *testing.B) {
			runRandomizedOperations(sub, s, 1000, bm.percentages)
		})
	}

	// run deterministic operations subbenchmarks for various scenarios
	c := counts{has: 200, get: 5500, set: 4000, delete: 300}
	sampledCounts := []counts{c}
	benchmarks = generateBenchmarks(dbBackendTypes, nil, sampledCounts)
	for _, bm := range benchmarks {
		db, err := newDB(version, bm.name, bm.dbType, dir)
		require.NoError(b, err)
		s, err := newStore(version, db)
		require.NoError(b, err)
		b.Run(bm.name, func(sub *testing.B) {
			runDeterministicOperations(sub, s, values, bm.counts)
		})
	}

	// test performance when the store reverting to the last committed version
	committedValues := values
	uncommittedValues := prepareValues(5000)
	for _, dbType := range dbBackendTypes {
		s, db := prepareStore(b, version, dbType, committedValues)
		b.Run(fmt.Sprintf("v%d-%s-revert", version, dbType), func(sub *testing.B) {
			s = RunRvert(sub, s, db, uncommittedValues)
		})
	}
}

func (p percentages) String() string {
	return fmt.Sprintf("r-%d-%d-%d-%d", p.has, p.get, p.set, p.delete)
}

func (ct counts) String() string {
	return fmt.Sprintf("d-%d-%d-%d-%d", ct.has, ct.get, ct.set, ct.delete)
}
