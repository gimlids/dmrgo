package dmrgo

// Logic for running our map/reduce jobs
// Copyright (c) 2011 Damian Gryski <damian@gryski.com>
// License: GPLv3 or, at your option, any later version

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KeyValue is the primary type for interacting with Hadoop.
type KeyValue struct {
	ReduceKey string
	SortKey   string
	Value     string
}

func readLineValue(br *bufio.Reader) (*KeyValue, error) {
	s, err := br.ReadString('\n')
	s = strings.TrimRight(s, "\n")
	if err != nil {
		return nil, err
	}
	// this appears to be a punt for handling case where mapper input already has one or more key
	return &KeyValue{"", "", s}, err
}

func readLineKeyValue(br *bufio.Reader) (*KeyValue, error) {

	k, err := br.ReadString('\t')
	if err != nil {
		return nil, err
	}
	k = strings.TrimRight(k, "\t")

	keys := strings.SplitN(k, ",", 2)

	var reduceKey string
	var sortKey string
	reduceKey, err = url.QueryUnescape(keys[0])
	if err != nil {
		return nil, err
	}

	if len(keys) == 2 {
		sortKey, err = url.QueryUnescape(keys[1])
		if err != nil {
			return nil, err
		}
	}

	v, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}

	v = strings.TrimRight(v, "\n")

	return &KeyValue{reduceKey, sortKey, v}, nil
}

// MapReduceJob is the interface expected by the job runner
type MapReduceJob interface {
	Map(key string, value string, emitter Emitter)

	// Called at the end of the Map phase
	MapFinal(emitter Emitter)

	Reduce(reduceKey string, sortKey string, values <-chan string, emitter Emitter)
}

// are in we in the map or reduce phase?
var optDoMap bool
var optDoReduce bool

// or the full map/reduce code
var optDoMapReduce bool

// use a secondary sort key
// var optSecondaryKey bool

// how many output partitions should we use
var optNumPartitions int

// how many concurrent mappers should we try to use
var optNumMappers int

// how many concurrent reducers should we try to use
var optNumReducers int

func init() {
	flag.BoolVar(&optDoMap, "mapper", false, "run mapper code on stdin")
	flag.BoolVar(&optDoReduce, "reducer", false, "run reducer on stdin")
	// flag.BoolVar(&optSecondaryKey, "with-secondary-key", false, "group by primary key, sort by url-encoded secondary key (because we have to separate the two keys with a comma)")
	flag.IntVar(&optNumPartitions, "partitions", 1, "parition data into sets")
	flag.BoolVar(&optDoMapReduce, "mapreduce", false, "run full map/reduce")
	flag.IntVar(&optNumMappers, "mappers", 4, "number of map processes")
	flag.IntVar(&optNumReducers, "reducers", 4, "number of reducer processes")
}

func mapreduce(mrjob MapReduceJob) {

	attr := new(os.ProcAttr)
	attr.Files = []*os.File{nil, nil, nil}

	pid := os.Getpid()

	wg := new(sync.WaitGroup)

	mapperInputFiles := flag.Args()

	// no input files -- read from stdin
	if len(mapperInputFiles) == 0 {
		mEmit := newPartitionEmitter(uint(optNumPartitions), fmt.Sprintf("tmp-map-out-p%d-f0", pid))
		mapper(mrjob, os.Stdin, mEmit)
		mapperFinal(mrjob, mEmit)
		mEmit.Flush()
		mEmit.Close()
		mapperInputFiles = []string{"(stdin)"}
	} else {
		// we have multiple input files -- run up to 'mappers' of them in parallel

		// the type of our channel -- limit scope 'cause we don't need it anywhere else
		type mapperFile struct {
			index int
			fname string
		}

		mapperWork := make(chan *mapperFile)

		// launch the goroutines
		for i := 0; i < optNumMappers; i++ {
			wg.Add(1)
			go func(inputs chan *mapperFile) {

				for input := range inputs {

					f, err := os.Open(input.fname)
					if err != nil {
						fmt.Fprintln(os.Stderr, "err opening ", f, ": ", err)
						return
					}

					mEmit := newPartitionEmitter(uint(optNumPartitions), fmt.Sprintf("tmp-map-out-p%d-f%d", pid, input.index))
					mapper(mrjob, f, mEmit)
					mEmit.Flush()
					mEmit.Close()
					f.Close()
				}
				wg.Done()
			}(mapperWork)
		}

		// and send the work
		for i, fname := range mapperInputFiles {
			mapperWork <- &mapperFile{i, fname}
		}
		close(mapperWork)

		wg.Wait()

		// then launch mapperFinal
		mEmit := newPartitionEmitter(uint(optNumPartitions), fmt.Sprintf("tmp-map-out-p%d-f%d", pid, len(mapperInputFiles)))
		mapperFinal(mrjob, mEmit)
		mEmit.Flush()
		mEmit.Close()
	}

	partitions := make(chan int)

	for i := 0; i < optNumReducers; i++ {

		wg.Add(1)

		go func(work chan int) {

			for partition := range work {

				fns, _ := filepath.Glob(fmt.Sprintf("tmp-map-out-p%d-f*.%04d", pid, partition))

				redin := fmt.Sprintf("tmp-red-in-p%d.%04d", pid, partition)

				cmdline := []string{"sort", "-o", redin}
				cmdline = append(cmdline, fns...)

				// sort
				p, err := os.StartProcess("/usr/bin/sort", cmdline, attr)
				if err != nil {
					fmt.Fprintln(os.Stderr, "err running sort: ", err)
				}
				p.Wait()

				// reduce
				f, _ := os.Open(redin)
				rout, _ := os.Create(fmt.Sprintf("red-out-p%d.%04d", pid, partition))
				rEmit := newPrintEmitter(bufio.NewWriter(rout))
				reducer(mrjob, f, rEmit)
				for _, fn := range fns {
					os.Remove(fn)
				}
				os.Remove(redin)
				rEmit.Flush()
				rout.Close()
			}
			wg.Done()
		}(partitions)
	}

	for i := 0; i < optNumPartitions; i++ {
		partitions <- i
	}
	close(partitions)

	wg.Wait()

	if optNumPartitions == 1 {
		fmt.Printf("output is in: red-out-p%d.0000\n", pid)
	} else {
		fmt.Printf("output is in: red-out-p%d.0000 - red-out-p%d.%04d\n", pid, pid, optNumPartitions-1)
	}
}

// Main runs the map reduce job passed in
func Main(mrjob MapReduceJob) {

	if optDoMapReduce {
		mapreduce(mrjob)
		return
	}

	if optDoMap && optDoReduce {
		fmt.Println("can either map or reduce, not both. (Did  you mean --mapreduce ?)")
		os.Exit(1)
	}

	if !optDoMap && !optDoReduce {
		fmt.Println("neither map nor reduce nor secondary key reduce called")
		os.Exit(1)
	}

	stdout := bufio.NewWriter(os.Stdout)

	emitter := newPrintEmitter(stdout)

	if optDoMap {
		mapper(mrjob, os.Stdin, emitter)
		// handle any finalization from the mapper
		mapperFinal(mrjob, emitter)
	}

	if optDoReduce {
		reducer(mrjob, os.Stdin, emitter)
	}

	emitter.Flush()
}

// run the mapping phase, calling the map routine on key/value pairs from the Reader
// The users' Map routine will write any key/value pairs generated to the Emitter
func mapper(mrjob MapReduceJob, r io.Reader, emitter Emitter) {

	br := bufio.NewReader(r)

	for {
		kv, err := readLineValue(br)
		if err != nil {
			break
		}

		mrjob.Map("", kv.Value, emitter)
	}
}

// run the cleanup phase for the mapper
func mapperFinal(mrjob MapReduceJob, emitter Emitter) {
	mrjob.MapFinal(emitter)
}

// run the reduce phase, calling the reduce routine on key/[]value read the Reader.
// We aggregate the values that have been mapped with the same key, then call the users' Reduce function.
// The users' Reduce routine will output any key/value pairs via the Emitter.
func reducer(mrjob MapReduceJob, r io.Reader, emitter Emitter) {

	br := bufio.NewReader(r)

	var currentReduceKey string
	var values chan string

	isFirstRun := true
	var done chan bool

	for {

		mkv, err := readLineKeyValue(br)
		if err != nil {
			break
		}

		if currentReduceKey != mkv.ReduceKey || isFirstRun {
			if !isFirstRun {
				close(values)
				<-done
			}
			isFirstRun = false
			values = make(chan string, 64)
			done = make(chan bool)
			go func() {
				mrjob.Reduce(mkv.ReduceKey, mkv.SortKey, values, emitter)
				done <- true
				close(done)
			}()
			currentReduceKey = mkv.ReduceKey
		}
		values <- mkv.Value
	}

	close(values)
	<-done
}
