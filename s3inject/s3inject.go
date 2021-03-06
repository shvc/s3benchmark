// s3-benchmark.go
// Copyright (c) 2017 Wasabi Technology, Inc.

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// version
var version = "2.1.1"

// TODO: avoid different threads download the same object

func logit(msg string) {
	fmt.Println(msg)
	logfile, _ := os.OpenFile("s3inject.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if logfile != nil {
		logfile.WriteString(time.Now().Format(http.TimeFormat) + ": " + msg + "\n")
		logfile.Close()
	}
}

func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	return hostname
}

// HTTPTransport represent Our HTTP transport used for the roundtripper below
var HTTPTransport http.RoundTripper = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	Dial: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).Dial,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 0,
	// Allow an unlimited number of idle connections
	MaxIdleConnsPerHost: 4096,
	MaxIdleConns:        0,
	// But limit their idle time
	IdleConnTimeout: time.Minute,
	// Ignore TLS errors
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}

func getS3Client(accessKey, secretKey, region, endpoint string) *s3.S3 {
	// Build our config
	creds := credentials.NewStaticCredentials(accessKey, secretKey, "")
	loglevel := aws.LogOff
	// Build the rest of the configuration
	awsConfig := &aws.Config{
		Region:               aws.String(region),
		Endpoint:             aws.String(endpoint),
		Credentials:          creds,
		LogLevel:             &loglevel,
		S3ForcePathStyle:     aws.Bool(true),
		S3Disable100Continue: aws.Bool(true),
		// Comment following to use default transport
		HTTPClient: &http.Client{Transport: HTTPTransport},
	}
	session := session.New(awsConfig)
	client := s3.New(session)
	if client == nil {
		log.Fatalf("FATAL: Unable to create new client.")
	}
	return client
}

func createBucket(accessKey, secretKey, region, endpoint, bucket string) {
	// Get a client
	client := getS3Client(accessKey, secretKey, region, endpoint)
	// Create our bucket (may already exist without error)
	in := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if _, err := client.CreateBucket(in); err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			switch awsErr.Code() {
			case "BucketAlreadyOwnedByYou":
				fallthrough
			case "BucketAlreadyExists":
				log.Printf("WARNING: Bucket:%s already exists", bucket)
				return
			}
		}
		log.Fatalf("FATAL: Unable to create bucket %s (is your access and secret correct?): %v", bucket, err)
	}
}

func deleteAllObjects(accessKey, secretKey, region, endpoint, bucket, prefix string) {
	// Get a client
	client := getS3Client(accessKey, secretKey, region, endpoint)
	// Use multiple routines to do the actual delete
	var wg sync.WaitGroup
	var keyMarker *string
	var err error
	for loop := 1; ; loop++ {
		// Delete all the existing objects in the bucket
		in := &s3.ListObjectsInput{
			Bucket:  aws.String(bucket),
			Marker:  keyMarker,
			Prefix:  aws.String(prefix),
			MaxKeys: aws.Int64(1000),
		}
		if listObjects, listErr := client.ListObjects(in); listErr == nil {
			delete := &s3.Delete{Quiet: aws.Bool(true)}
			for _, obj := range listObjects.Contents {
				delete.Objects = append(delete.Objects, &s3.ObjectIdentifier{Key: obj.Key})
			}
			if len(delete.Objects) > 0 {
				wg.Add(1)
				go func(bucket string, delete *s3.Delete) {
					if _, e := client.DeleteObjects(&s3.DeleteObjectsInput{
						Bucket: aws.String(bucket),
						Delete: delete}); e != nil {
						err = fmt.Errorf("DeleteObjects unexpected failure: %s", e.Error())
					}
					wg.Done()
				}(bucket, delete)
			}
			// Advance to next page
			if listObjects.IsTruncated == nil || !*listObjects.IsTruncated {
				break
			}
			keyMarker = listObjects.NextMarker
		} else {
			// The bucket may not exist, just ignore in that case
			if strings.HasPrefix(listErr.Error(), "NoSuchBucket") {
				return
			}
			err = fmt.Errorf("ListObjects unexpected failure: %v", listErr)
			break
		}
	}
	// Wait for deletes to finish
	wg.Wait()
	// If error, it is fatal
	if err != nil {
		log.Fatalf("FATAL: Unable to delete objects from bucket: %v", err)
	}
}

// canonicalAmzHeaders -- return the x-amz headers canonicalized
func canonicalAmzHeaders(req *http.Request) string {
	// Parse out all x-amz headers
	var headers []string
	for header := range req.Header {
		norm := strings.ToLower(strings.TrimSpace(header))
		if strings.HasPrefix(norm, "x-amz") {
			headers = append(headers, norm)
		}
	}
	// Put them in sorted order
	sort.Strings(headers)
	// Now add back the values
	for n, header := range headers {
		headers[n] = header + ":" + strings.Replace(req.Header.Get(header), "\n", " ", -1)
	}
	// Finally, put them back together
	if len(headers) > 0 {
		return strings.Join(headers, "\n") + "\n"
	}
	return ""
}

func hmacSHA1(key []byte, content string) []byte {
	mac := hmac.New(sha1.New, key)
	mac.Write([]byte(content))
	return mac.Sum(nil)
}

func setSignature(accessKey, secretKey string, req *http.Request) {
	// Setup default parameters
	dateHdr := time.Now().UTC().Format(time.RFC1123)
	req.Header.Set("X-Amz-Date", dateHdr)
	// Get the canonical resource and header
	canonicalResource := req.URL.EscapedPath()
	canonicalHeaders := canonicalAmzHeaders(req)
	stringToSign := req.Method + "\n" + req.Header.Get("Content-MD5") + "\n" + req.Header.Get("Content-Type") + "\n\n" +
		canonicalHeaders + canonicalResource
	hash := hmacSHA1([]byte(secretKey), stringToSign)
	signature := base64.StdEncoding.EncodeToString(hash)
	req.Header.Set("Authorization", fmt.Sprintf("AWS %s:%s", accessKey, signature))
}

func main() {
	var accessKey, secretKey, endpoint, bucket, region string
	var number int64
	var threads uint

	flag.StringVar(&accessKey, "a", "object_user1", "Access key")
	flag.StringVar(&secretKey, "s", "ChangeMeChangeMeChangeMeChangeMeChangeMe", "Secret key")
	flag.StringVar(&endpoint, "e", "http://192.168.55.2:9020", "S3 Endpoint URL")
	flag.StringVar(&bucket, "b", "s3inject-test", "Bucket for testing")
	flag.StringVar(&region, "r", endpoints.CnNorth1RegionID, "Region for testing")
	flag.Int64Var(&number, "n", 1024, "Number of random file to inject")
	flag.UintVar(&threads, "t", 16, "Number of threads to run")
	sizeArg := flag.String("z", "128K", "Size of objects in bytes with postfix K, M, and G")
	flag.Parse()

	// Check the arguments
	if accessKey == "" {
		log.Fatal("Missing argument -a for access key.")
	}
	if secretKey == "" {
		log.Fatal("Missing argument -s for secret key.")
	}

	if number < 1 {
		log.Fatal("Invalid number of files")
	}

	objectSize, err := bytefmt.ToBytes(*sizeArg)
	if err != nil {
		log.Fatalf("Invalid -z argument for object size: %v", err)
	}
	hostname := getHostname()
	//var objectDataMd5 string

	fmt.Printf("s3benchmark %s v%s\n", hostname, version)
	logit(fmt.Sprintf("url=%s, bucket=%s, region=%s, number=%d, threads=%d, size=%s(%d)",
		endpoint, bucket, region, number, threads, *sizeArg, objectSize))

	// Initialize data
	objectData := make([]byte, objectSize)
	if n, e := rand.Read(objectData); e != nil {
		log.Fatalf("generate random data failed: %s", e)
	} else if uint64(n) < objectSize {
		log.Fatalf("invalid randome data size, got %d, expect %d", n, objectSize)
	}

	//hasher := md5.New()
	//hasher.Write(objectData)
	//objectDataMd5 = base64.StdEncoding.EncodeToString(hasher.Sum(nil))

	// Create the Bucket and delete the Objects
	createBucket(accessKey, secretKey, region, endpoint, bucket)
	deleteAllObjects(accessKey, secretKey, region, endpoint, bucket, hostname)
	httpClient := &http.Client{Transport: HTTPTransport}

	// Loop running the tests
	logit("   Objects\tElapsed(s)\t Throuphput\t   TPS\t Failed")

	var uploadCount, uploadFailedCount int64

	var uploadFinish time.Time
	starttime := time.Now()
	wg := sync.WaitGroup{}
	for n := uint(1); n <= threads; n++ {
		wg.Add(1)
		go func() {
			for {
				objnum := atomic.AddInt64(&uploadCount, 1)
				if objnum > number {
					break
				}
				fileobj := bytes.NewReader(objectData)
				prefix := fmt.Sprintf("%s/%s/k_%s_%d", endpoint, bucket, hostname, objnum)
				req, _ := http.NewRequest(http.MethodPut, prefix, fileobj)
				req.Header.Set("Content-Length", strconv.FormatUint(objectSize, 10))
				//req.Header.Set("Content-MD5", objectDataMd5)
				setSignature(accessKey, secretKey, req)
				if resp, err := httpClient.Do(req); err != nil {
					log.Fatalf("FATAL: Error uploading object %s: %v", prefix, err)
				} else if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
					atomic.AddInt64(&uploadFailedCount, 1)
					atomic.AddInt64(&uploadCount, -1)
					fmt.Printf("upload resp: %v\n", resp)
					if resp.StatusCode != http.StatusServiceUnavailable {
						fmt.Printf("Upload status %s: resp: %+v\n", resp.Status, resp)
						if resp.Body != nil {
							body, _ := ioutil.ReadAll(resp.Body)
							fmt.Printf("Body: %s\n", string(body))
						}
					}
				}
				time.Sleep(time.Second * 1)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	uploadFinish = time.Now()
	uploadTime := uploadFinish.Sub(starttime).Seconds()
	bps := float64(uint64(number)*objectSize) / uploadTime
	logit(fmt.Sprintf("%10d\t%10.1f\t%10sB\t%6.1f\t%7d",
		number, uploadTime, bytefmt.ByteSize(uint64(bps)), float64(number)/uploadTime, uploadFailedCount))

}
