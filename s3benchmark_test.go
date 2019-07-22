package main

import (
	"fmt"
	"net/http"
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

func Test_canonicalAmzHeaders(t *testing.T) {
	expect := "x-amz-a:content-a\nx-amz-b:content-b\nx-amz-c:content-c\n"
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/bucket/key", nil)
	req.Header.Set("X-Amz-c", "content-c")
	req.Header.Set("X-Xmz-D", "content-d")
	req.Header.Set("X-Amz-A", "content-a")
	req.Header.Set("X-Amz-B", "content-b")
	result := canonicalAmzHeaders(req)
	if result != expect {
		t.Errorf("expect %s, but got %s", expect, result)
	}
}
