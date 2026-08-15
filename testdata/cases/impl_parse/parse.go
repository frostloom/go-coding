package parsekv

import (
	"errors"
)

// Parse parses a semicolon-separated list of key=value pairs into a map.
//
//	Parse("a=1;b=2")           -> {"a":"1","b":"2"}, nil
//	Parse("a = 1 ; b = 2")     -> {"a":"1","b":"2"}, nil (whitespace trimmed)
//	Parse("")                  -> {}, nil (empty input is fine)
//	Parse("a=1;")              -> {"a":"1"}, nil   (empty segment ignored)
//	Parse("a=1;y")             -> error (segment missing '=')
//	Parse("a=1;=2")            -> error (empty key)
//
// Currently a stub that always fails — implement it.
func Parse(s string) (map[string]string, error) {
	return nil, errors.New("not implemented")
}
