package dmrgo

// Protocols for un/marshaling stream values
// Copyright (c) 2011 Damian Gryski <damian@gryski.com>
// License: GPLv3 or, at your option, any later version

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// StreamProtocol is a set of routines for marshaling and unmarshaling key/value pairs from the input stream.
// Map Reduce jobs can define their own protocols.
type StreamProtocol interface {

	// UnmarshalKV turns strings into their associated values.
	// k should be a pointer to the destination value for the unmarshalled "key"
	// vs should be a pointer to an array for the unmarshalled "values"
	UnmarshalKVs(key string, values []string, k interface{}, vs interface{})

	// MarshalKV turns a key/value pair into a KeyValue
	Marshal(reduceKey interface{}, sortKey interface{}, value interface{}) *KeyValue
}

// JSONProtocol parse input/output values as JSON strings
type JSONProtocol struct {
	// empty -- just a type
}

// UnmarshalKVs implements the StreamProtocol interface
func (p *JSONProtocol) UnmarshalKVs(key string, values []string, k interface{}, vs interface{}) {

	json.Unmarshal([]byte(key), &k)

	vsPtrValue := reflect.ValueOf(vs)
	vsType := reflect.TypeOf(vs).Elem()

	v := reflect.MakeSlice(vsType, len(values), len(values))

	for i, js := range values {
		e := v.Index(i)
		err := json.Unmarshal([]byte(js), e.Addr().Interface())
		if err != nil {
			// skip, for now
			continue
		}
	}

	vsPtrValue.Elem().Set(v)
}

// MarshalKV implements the StreamProtocol interface
func (p *JSONProtocol) Marshal(reduceKey interface{}, sortKey interface{}, value interface{}) *KeyValue {
	r, _ := json.Marshal(reduceKey)
	s, _ := json.Marshal(sortKey)
	v, _ := json.Marshal(value)
	return &KeyValue{string(r), string(s), string(v)}
}

// TSVProtocol outputs keys as tab-separated lines
type TSVProtocol struct {
	// empty -- just a type
}

// MarshalKV implements the StreamProtocol interface
func (p *TSVProtocol) MarshalKV(reduceKey interface{}, sortKey interface{}, value interface{}) *KeyValue {

	var vs []string

	vType := reflect.TypeOf(value)
	vVal := reflect.ValueOf(value)

	if vType.Kind() == reflect.Struct {
		vs = make([]string, vType.NumField())
		for i := 0; i < vType.NumField(); i++ {
			field := vVal.Field(i)
			vs[i] = primitiveToString(field)
		}
	} else if isPrimitive(vType.Kind()) {
		vs = append(vs, primitiveToString(vVal))
	} else if vType.Kind() == reflect.Array || vType.Kind() == reflect.Slice {
		vs = make([]string, vVal.Len())
		for i := 0; i < vVal.Len(); i++ {
			field := vVal.Index(i)
			// arrays/slices must be of primitives
			vs[i] = primitiveToString(field)
		}
	}

	vals := strings.Join(vs, "\t")

	reduceKeyVal := reflect.ValueOf(reduceKey)
	r := primitiveToString(reduceKeyVal)

	sortKeyVal := reflect.ValueOf(reduceKey)
	s := primitiveToString(sortKeyVal)

	return &KeyValue{r, s, vals}
}

// UnmarshalKVs implements the StreamProtocol interface
func (p *TSVProtocol) UnmarshalKVs(key string, values []string, k interface{}, vs interface{}) {

	fmt.Sscan(key, &k)

	vsPtrValue := reflect.ValueOf(vs)
	vsType := reflect.TypeOf(vs).Elem()
	vType := vsType.Elem()

	v := reflect.MakeSlice(vsType, len(values), len(values))

	for vi, s := range values {
		vs := strings.Split(s, "\t")

		// create our new element
		e := v.Index(vi)

		// figure out what kind we need to unpack our data into
		if vType.Kind() == reflect.Struct {
			for i := 0; i < vType.NumField(); i++ {
				_, err := fmt.Sscan(vs[i], e.Field(i).Addr().Interface())
				if err != nil {
					continue // skip
				}
			}
		} else if vType.Kind() == reflect.Array {
			for i := 0; i < vType.Len(); i++ {
				_, err := fmt.Sscan(vs[i], e.Index(i).Addr().Interface())
				if err != nil {
					continue // skip
				}
			}
		} else if isPrimitive(vType.Kind()) {
			fmt.Sscan(vs[0], e.Addr().Interface())
		}
	}

	vsPtrValue.Elem().Set(v)
}

func isPrimitive(k reflect.Kind) bool {

	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	}

	return false
}

func primitiveToString(v reflect.Value) string {

	switch v.Kind() {

	case reflect.Bool:
		if v.Bool() {
			return "1"
		}
		return "0"

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)

	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', 5, 64)
	case reflect.String:
		return v.String()
	}

	return "(unknown type " + string(v.Kind()) + ")"
}
