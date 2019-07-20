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
	"io"
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
var version = "1.1.1"

// TODO: avoid different threads download the same object

// Global variables
var accessKey, secretKey, endpoint, bucket, region string
var durationSecs, threads, loops int
var objectSize uint64
var objectData []byte

//var objectDataMd5 string
var runningThreads, uploadCount, downloadCount, deleteCount, uploadFailedCount, downloadFailedCount, deleteFailedCount int32
var endtime, uploadFinish, downloadFinish, deleteFinish time.Time

func logit(msg string) {
	fmt.Println(msg)
	logfile, _ := os.OpenFile("s3benchmark.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
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

var httpClient = &http.Client{Transport: HTTPTransport}

func getS3Client() *s3.S3 {
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

func createBucket() {
	// Get a client
	client := getS3Client()
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

func deleteAllObjects() {
	// Get a client
	client := getS3Client()
	// Use multiple routines to do the actual delete
	var doneDeletes sync.WaitGroup
	// Loop deleting reading as big a list as we can
	var keyMarker *string
	var err error
	for loop := 1; ; loop++ {
		// Delete all the existing objects and versions in the bucket
		in := &s3.ListObjectsInput{
			Bucket:  aws.String(bucket),
			Marker:  keyMarker,
			MaxKeys: aws.Int64(1000),
		}
		if listObjects, listErr := client.ListObjects(in); listErr == nil {
			delete := &s3.Delete{Quiet: aws.Bool(true)}
			for _, obj := range listObjects.Contents {
				delete.Objects = append(delete.Objects, &s3.ObjectIdentifier{Key: obj.Key})
			}
			if len(delete.Objects) > 0 {
				// Start a delete routine
				doDelete := func(bucket string, delete *s3.Delete) {
					if _, e := client.DeleteObjects(&s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: delete}); e != nil {
						err = fmt.Errorf("DeleteObjects unexpected failure: %s", e.Error())
					}
					doneDeletes.Done()
				}
				doneDeletes.Add(1)
				go doDelete(bucket, delete)
			}
			// Advance to next versions
			if listObjects.IsTruncated == nil || !*listObjects.IsTruncated {
				break
			}
			keyMarker = listObjects.NextMarker
		} else {
			// The bucket may not exist, just ignore in that case
			if strings.HasPrefix(listErr.Error(), "NoSuchBucket") {
				return
			}
			err = fmt.Errorf("ListObjectVersions unexpected failure: %v", listErr)
			break
		}
	}
	// Wait for deletes to finish
	doneDeletes.Wait()
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

func setSignature(req *http.Request) {
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

func runUpload(threadNum int) {
	for time.Now().Before(endtime) {
		objnum := atomic.AddInt32(&uploadCount, 1)
		fileobj := bytes.NewReader(objectData)
		prefix := fmt.Sprintf("%s/%s/Object-%d", endpoint, bucket, objnum)
		req, _ := http.NewRequest(http.MethodPut, prefix, fileobj)
		req.Header.Set("Content-Length", strconv.FormatUint(objectSize, 10))
		//req.Header.Set("Content-MD5", objectDataMd5)
		setSignature(req)
		if resp, err := httpClient.Do(req); err != nil {
			log.Fatalf("FATAL: Error uploading object %s: %v", prefix, err)
		} else if resp != nil && resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusServiceUnavailable {
				atomic.AddInt32(&uploadFailedCount, 1)
				atomic.AddInt32(&uploadCount, -1)
			} else {
				fmt.Printf("Upload status %s: resp: %+v\n", resp.Status, resp)
				if resp.Body != nil {
					body, _ := ioutil.ReadAll(resp.Body)
					fmt.Printf("Body: %s\n", string(body))
				}
			}
		}
	}
	// Remember last done time
	uploadFinish = time.Now()
	// One less thread
	atomic.AddInt32(&runningThreads, -1)
}

func runDownload(threadNum int) {
	for time.Now().Before(endtime) {
		atomic.AddInt32(&downloadCount, 1)
		objnum := rand.Int31n(uploadCount) + 1
		prefix := fmt.Sprintf("%s/%s/Object-%d", endpoint, bucket, objnum)
		req, _ := http.NewRequest(http.MethodGet, prefix, nil)
		setSignature(req)
		if resp, err := httpClient.Do(req); err != nil {
			log.Fatalf("FATAL: Error downloading object %s: %v", prefix, err)
		} else if resp != nil && resp.Body != nil {
			if resp.StatusCode == http.StatusServiceUnavailable {
				atomic.AddInt32(&downloadFailedCount, 1)
				atomic.AddInt32(&downloadCount, -1)
			} else {
				io.Copy(ioutil.Discard, resp.Body)
			}
		}
	}
	// Remember last done time
	downloadFinish = time.Now()
	// One less thread
	atomic.AddInt32(&runningThreads, -1)
}

func runDelete(threadNum int) {
	for {
		objnum := atomic.AddInt32(&deleteCount, 1)
		if objnum > uploadCount {
			break
		}
		prefix := fmt.Sprintf("%s/%s/Object-%d", endpoint, bucket, objnum)
		req, _ := http.NewRequest(http.MethodDelete, prefix, nil)
		setSignature(req)
		if resp, err := httpClient.Do(req); err != nil {
			log.Fatalf("FATAL: Error deleting object %s: %v", prefix, err)
		} else if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
			atomic.AddInt32(&deleteFailedCount, 1)
			atomic.AddInt32(&deleteCount, -1)
		}
	}
	// Remember last done time
	deleteFinish = time.Now()
	// One less thread
	atomic.AddInt32(&runningThreads, -1)
}

func main() {
	flag.StringVar(&accessKey, "a", "object_user1", "Access key")
	flag.StringVar(&secretKey, "s", "ChangeMeChangeMeChangeMeChangeMeChangeMe", "Secret key")
	flag.StringVar(&endpoint, "e", "http://192.168.55.2:9020", "S3 Endpoint URL")
	flag.StringVar(&bucket, "b", "s3benchmark-test", "Bucket for testing")
	flag.StringVar(&region, "r", endpoints.CnNorth1RegionID, "Region for testing")
	flag.IntVar(&durationSecs, "d", 60, "Duration of each test in seconds")
	flag.IntVar(&threads, "t", 1, "Number of threads to run")
	flag.IntVar(&loops, "l", 1, "Number of times to repeat test")
	sizeArg := flag.String("z", "128K", "Size of objects in bytes with postfix K, M, and G")
	flag.Parse()

	// Check the arguments
	if accessKey == "" {
		log.Fatal("Missing argument -a for access key.")
	}
	if secretKey == "" {
		log.Fatal("Missing argument -s for secret key.")
	}
	var err error
	if objectSize, err = bytefmt.ToBytes(*sizeArg); err != nil {
		log.Fatalf("Invalid -z argument for object size: %v", err)
	}

	fmt.Printf("s3benchmark v%s\n", version)
	logit(fmt.Sprintf("url=%s, bucket=%s, region=%s, duration=%d, threads=%d, loops=%d, size=%s(%d)",
		endpoint, bucket, region, durationSecs, threads, loops, *sizeArg, objectSize))

	// Initialize data
	objectData = make([]byte, objectSize)
	if n, e := rand.Read(objectData); e != nil {
		log.Fatalf("generate random data failed: %s", e)
	} else if uint64(n) < objectSize {
		log.Fatalf("invalid randome data size, got %d, expect %d", n, objectSize)
	}

	//hasher := md5.New()
	//hasher.Write(objectData)
	//objectDataMd5 = base64.StdEncoding.EncodeToString(hasher.Sum(nil))

	// Create the bucket and delete all the objects
	createBucket()
	deleteAllObjects()

	// Loop running the tests
	logit("Loop\tMethod\t  Objects\tElapsed(s)\t Throuphput\t   TPS\t Failed")
	for loop := 1; loop <= loops; loop++ {
		// reset counters
		uploadCount = 0
		uploadFailedCount = 0
		downloadCount = 0
		downloadFailedCount = 0
		deleteCount = 0
		deleteFailedCount = 0
		runningThreads = int32(threads)
		starttime := time.Now()
		endtime = starttime.Add(time.Second * time.Duration(durationSecs))
		for n := 1; n <= threads; n++ {
			go runUpload(n)
		}
		// Wait for it to finish
		for atomic.LoadInt32(&runningThreads) > 0 {
			time.Sleep(time.Millisecond)
		}
		uploadTime := uploadFinish.Sub(starttime).Seconds()
		bps := float64(uint64(uploadCount)*objectSize) / uploadTime
		logit(fmt.Sprintf("%4d\t%6s\t%9d\t%10.1f\t%10sB\t%6.1f\t%7d",
			loop, http.MethodPut, uploadCount, uploadTime, bytefmt.ByteSize(uint64(bps)), float64(uploadCount)/uploadTime, uploadFailedCount))

		// Run the download case
		runningThreads = int32(threads)
		starttime = time.Now()
		endtime = starttime.Add(time.Second * time.Duration(durationSecs))
		for n := 1; n <= threads; n++ {
			go runDownload(n)
		}
		// Wait for it to finish
		for atomic.LoadInt32(&runningThreads) > 0 {
			time.Sleep(time.Millisecond)
		}
		downloadTime := downloadFinish.Sub(starttime).Seconds()
		bps = float64(uint64(downloadCount)*objectSize) / downloadTime
		logit(fmt.Sprintf("%4d\t%6s\t%9d\t%10.1f\t%10sB\t%6.1f\t%7d",
			loop, http.MethodGet, downloadCount, downloadTime, bytefmt.ByteSize(uint64(bps)), float64(downloadCount)/downloadTime, downloadFailedCount))

		// Run the delete case
		runningThreads = int32(threads)
		starttime = time.Now()
		endtime = starttime.Add(time.Second * time.Duration(durationSecs))
		for n := 1; n <= threads; n++ {
			go runDelete(n)
		}
		// Wait for it to finish
		for atomic.LoadInt32(&runningThreads) > 0 {
			time.Sleep(time.Millisecond)
		}
		deleteTime := deleteFinish.Sub(starttime).Seconds()
		logit(fmt.Sprintf("%4d\t%6s\t%9d\t%10.1f\t%11s\t%6.1f\t%7d",
			loop, http.MethodDelete, deleteCount, deleteTime, "NaN", float64(uploadCount)/deleteTime, deleteFailedCount))
	}

	// All done
	fmt.Println("Benchmark completed.")
}
