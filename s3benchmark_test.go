package main

import (
	"fmt"
	"testing"
)

func Test_hmacSHA1(t *testing.T) {
	cases := map[string][2]string{
		"46b4ec586117154dacd49d664e5d63fdc88efb51": [2]string{"foo", "bar"},
		"68d880c3499b7e53e6ef5b0bd92ad723244fc591": [2]string{"s3", "bucket"},
	}
	for result, ti := range cases {
		to := fmt.Sprintf("%x", hmacSHA1([]byte(ti[0]), ti[1]))
		if to != result {
			t.Errorf("expect: %s, but got: %s", result, to)
		}
	}
}
