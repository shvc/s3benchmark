package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// summary statistics used to summarize first byte and last byte latencies
type stat int

const (
	min stat = iota + 1
	max
	avg
	p25
	p50
	p75
	p90
	p99
)

// represents the duration from making an S3 GetObject request to getting the first byte and last byte
type latency struct {
	FirstByte time.Duration
	LastByte  time.Duration
}

// a benchmark record for one object size and thread count
type benchmark struct {
	objectSize uint64
	threads    int
	firstByte  map[stat]float64
	lastByte   map[stat]float64
	dataPoints []latency
}

// absolute limits
const maxPayload = 18
const maxThreads = 64

// default settings
const defaultRegion = "us-west-2"

// the hostname or EC2 instance id
var hostname = getHostname()

// the EC2 instance region if available
var region string

// the endpoint URL if applicable
var endpoint, accessKey, secretKey string

// the script will automatically create an S3 bucket to use for the test, and it tries to get a unique bucket name
// by generating a sha hash of the hostname
var bucketName string

// the min and max object sizes to test - 1 = 1 KB, and the size doubles with every increment
var payloadsMin int
var payloadsMax int

// the min and max thread count to use in the test
var threadsMin int
var threadsMax int

// the number of samples to collect for each benchmark record
var samples int

// a test mode to find out when EC2 network throttling kicks in
var throttlingMode bool

// flag to cleanup the s3 bucket and exit the program
var cleanupOnly bool

// the S3 SDK client
var s3Client *s3.Client

var transport http.RoundTripper = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	Dial: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).Dial,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 0,
	MaxIdleConnsPerHost:   4096,
	MaxIdleConns:          0,
	IdleConnTimeout:       time.Minute,
	TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
	},
}

// program entry point
func main() {
	parseFlags()

	setupS3Client()

	if cleanupOnly {
		cleanup()
		return
	}

	setup() // upload 00000000

	runBenchmark()

	cleanup()
}

func parseFlags() {
	flag.IntVar(&threadsMin, "threads-min", 4, "The minimum number of threads to use when fetching objects from S3.")
	flag.IntVar(&threadsMax, "threads-max", 8, "The maximum number of threads to use when fetching objects from S3.")
	flag.IntVar(&payloadsMin, "size-min", 1, "The minimum object size to test, with 1 = 1 KB, and every increment is a double of the previous value.")
	flag.IntVar(&payloadsMax, "size-max", 4, "The maximum object size to test, with 1 = 1 KB, and every increment is a double of the previous value.")
	flag.IntVar(&samples, "samples", 256, "The number of samples to collect for each test of a single object size and thread count.")
	flag.StringVar(&bucketName, "bucket", "s3benchmark-test", "Cleans up all the S3 artifacts used by the benchmarks.")
	flag.StringVar(&region, "region", "", "Sets the AWS region to use for the S3 bucket.")
	flag.StringVar(&endpoint, "endpoint", "http://192.168.55.2:9020", "Sets the S3 endpoint to use. Only applies to non-AWS, S3-compatible stores.")
	flag.StringVar(&accessKey, "a", "object_user1", "Access key")
	flag.StringVar(&secretKey, "s", "ChangeMeChangeMeChangeMeChangeMeChangeMe", "Secret key")
	fullArg := flag.Bool("full", false, "Runs the full exhaustive test, and overrides the threads and payload arguments.")
	flag.BoolVar(&throttlingMode, "throttling-mode", false, "Runs a continuous test to find out when EC2 network throttling kicks in.")
	flag.BoolVar(&cleanupOnly, "cleanup", false, "Cleans all the objects uploaded to S3 for this test.")

	flag.Parse()

	if payloadsMin > payloadsMax {
		payloadsMin = payloadsMax
	}

	if threadsMin > threadsMax {
		threadsMin = threadsMax
	}

	if *fullArg {
		// if running the full exhaustive test, the threads and payload arguments get overridden with these
		threadsMin = 2
		threadsMax = 8
		payloadsMin = 1
		payloadsMax = 4
	}

	if throttlingMode {
		// if running the network throttling test, the threads and payload arguments get overridden with these
		threadsMin = 8
		threadsMax = 8
		payloadsMin = 8
		payloadsMax = 8
	}
}

func setupS3Client() {
	awsConfig := aws.Config{
		Region:        region,
		ClientLogMode: 0,
		HTTPClient: &http.Client{
			Transport: transport,
		},
		Credentials: aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     accessKey,
				SecretAccessKey: secretKey,
			}, nil
		}),
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(func(service, region string, opts ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:           endpoint,
				SigningName:   "s3",
				SigningRegion: region,
			}, nil
		}),
	}

	s3Client = s3.NewFromConfig(awsConfig, func(opts *s3.Options) {
		opts.UsePathStyle = true
	})

}

func setup() {
	fmt.Print("\n--- \033[1;32mSETUP\033[0m --------------------------------------------------------------------------------------------------------------------\n\n")

	// try to create the S3 bucket
	_, err := s3Client.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		},
	})

	// if the error is because the bucket already exists, ignore the error
	if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou:") {
		panic("Failed to create S3 bucket: " + err.Error())
	}

	generatePayload := payloadSizeGenerator()
	// loop over every payload size
	for p := 1; p <= payloadsMax; p++ {
		objectSize := generatePayload()
		// ignore payloads smaller than the min argument
		if p < payloadsMin {
			continue
		}
		fmt.Printf("Uploading \033[1;33m%-s\033[0m objects\n", byteFormat(float64(objectSize)))
		// create an object for every thread, so that different threads don't download the same object
		for t := 1; t <= threadsMax; t++ {
			// generate an S3 key from the sha hash of the hostname, thread index, and object size
			key := generateS3Key(hostname, t, objectSize)

			// do a HeadObject request to avoid uploading the object if it already exists from a previous test run
			_, err := s3Client.HeadObject(context.Background(), &s3.HeadObjectInput{
				Bucket: aws.String(bucketName),
				Key:    aws.String(key),
			})

			// if other error, exit
			if err != nil && !strings.Contains(err.Error(), "NotFound:") {
				panic("Failed to head S3 object: " + err.Error())
			}

			// generate empty payload
			payload := make([]byte, objectSize)

			// do a PutObject request to create the object
			_, err = s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
				Bucket: aws.String(bucketName),
				Key:    aws.String(key),
				Body:   bytes.NewReader(payload),
			})

			// if the put fails, exit
			if err != nil {
				panic("Failed to put S3 object: " + err.Error())
			}
		}

		fmt.Print("\n")
	}
}

func runBenchmark() {
	fmt.Print("\n--- \033[1;32mBENCHMARK\033[0m ----------------------------------------------------------------------------------------------------------------\n\n")

	// array of csv records used to upload the results to S3 when the test is finished
	var csvRecords [][]string

	generatePayload := payloadSizeGenerator()
	for p := 1; p <= payloadsMax; p++ {
		payload := generatePayload()
		if p < payloadsMin {
			continue
		}

		// print the header for the benchmark of this object size
		printHeader(payload)

		// run a test per thread count and object size combination
		for t := threadsMin; t <= threadsMax; t++ {
			// if throttling mode, loop forever
			for n := 1; true; n++ {
				csvRecords = execTest(t, payload, n, csvRecords)
				if !throttlingMode {
					break
				}
			}
		}
		fmt.Print("+---------+----------------+------------------------------------------------+------------------------------------------------+\n\n")
	}

}

func execTest(threadCount int, payloadSize uint64, runNumber int, csvRecords [][]string) [][]string {
	// this overrides the sample count on small hosts that can get overwhelmed by a large throughput
	samples := getTargetSampleCount(threadCount, samples)

	// a channel to submit the test tasks
	testTasks := make(chan int, threadCount)

	// a channel to receive results from the test tasks back on the main thread
	results := make(chan latency, samples)

	// create the workers for all the threads in this test
	for w := 1; w <= threadCount; w++ {
		go func(o int, tasks <-chan int, results chan<- latency) {
			for range tasks {
				key := generateS3Key(hostname, o, payloadSize)
				// start the timer to measure the first byte and last byte latencies
				latencyTimer := time.Now()

				resp, err := s3Client.GetObject(context.Background(), &s3.GetObjectInput{
					Bucket: aws.String(bucketName),
					Key:    aws.String(key),
				})
				if err != nil {

				}

				// measure the first byte latency
				firstByte := time.Now().Sub(latencyTimer)

				// read the s3 object body into the buffer
				if _, err := io.Copy(ioutil.Discard, resp.Body); err != nil {
					panic("Error reading object body: " + err.Error())
				}
				resp.Body.Close()

				// measure the last byte latency
				lastByte := time.Now().Sub(latencyTimer)

				// add the latency result to the results channel
				results <- latency{FirstByte: firstByte, LastByte: lastByte}
			}
		}(w, testTasks, results)
	}

	// start the timer for this benchmark
	benchmarkTimer := time.Now()

	// submit all the test tasks
	for j := 1; j <= samples; j++ {
		testTasks <- j
	}

	// close the channel
	close(testTasks)

	// construct a new benchmark record
	benchmarkRecord := benchmark{
		firstByte: make(map[stat]float64),
		lastByte:  make(map[stat]float64),
		threads:   threadCount,
	}
	sumFirstByte := int64(0)
	sumLastByte := int64(0)

	// wait for all the results to come and collect the individual datapoints
	for s := 1; s <= samples; s++ {
		timing := <-results
		benchmarkRecord.dataPoints = append(benchmarkRecord.dataPoints, timing)
		sumFirstByte += timing.FirstByte.Nanoseconds()
		sumLastByte += timing.LastByte.Nanoseconds()
		benchmarkRecord.objectSize += payloadSize
	}

	// stop the timer for this benchmark
	totalTime := time.Now().Sub(benchmarkTimer)

	// calculate the summary statistics for the first byte latencies
	sort.Sort(ByFirstByte(benchmarkRecord.dataPoints))
	benchmarkRecord.firstByte[avg] = (float64(sumFirstByte) / float64(samples)) / 1000000
	benchmarkRecord.firstByte[min] = float64(benchmarkRecord.dataPoints[0].FirstByte.Nanoseconds()) / 1000000
	benchmarkRecord.firstByte[max] = float64(benchmarkRecord.dataPoints[len(benchmarkRecord.dataPoints)-1].FirstByte.Nanoseconds()) / 1000000
	benchmarkRecord.firstByte[p25] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.25))-1].FirstByte.Nanoseconds()) / 1000000
	benchmarkRecord.firstByte[p50] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.5))-1].FirstByte.Nanoseconds()) / 1000000
	benchmarkRecord.firstByte[p75] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.75))-1].FirstByte.Nanoseconds()) / 1000000
	benchmarkRecord.firstByte[p90] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.90))-1].FirstByte.Nanoseconds()) / 1000000
	benchmarkRecord.firstByte[p99] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.99))-1].FirstByte.Nanoseconds()) / 1000000

	// calculate the summary statistics for the last byte latencies
	sort.Sort(ByLastByte(benchmarkRecord.dataPoints))
	benchmarkRecord.lastByte[avg] = (float64(sumLastByte) / float64(samples)) / 1000000
	benchmarkRecord.lastByte[min] = float64(benchmarkRecord.dataPoints[0].LastByte.Nanoseconds()) / 1000000
	benchmarkRecord.lastByte[max] = float64(benchmarkRecord.dataPoints[len(benchmarkRecord.dataPoints)-1].LastByte.Nanoseconds()) / 1000000
	benchmarkRecord.lastByte[p25] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.25))-1].LastByte.Nanoseconds()) / 1000000
	benchmarkRecord.lastByte[p50] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.5))-1].LastByte.Nanoseconds()) / 1000000
	benchmarkRecord.lastByte[p75] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.75))-1].LastByte.Nanoseconds()) / 1000000
	benchmarkRecord.lastByte[p90] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.90))-1].LastByte.Nanoseconds()) / 1000000
	benchmarkRecord.lastByte[p99] = float64(benchmarkRecord.dataPoints[int(float64(samples)*float64(0.99))-1].LastByte.Nanoseconds()) / 1000000

	// calculate the throughput rate
	rate := (float64(benchmarkRecord.objectSize)) / (totalTime.Seconds()) / 1024 / 1024

	// determine what to put in the first column of the results
	c := benchmarkRecord.threads
	if throttlingMode {
		c = runNumber
	}

	// print the results to stdout
	fmt.Printf("| %7d | \033[1;31m%9.1f MB/s\033[0m |%5.0f %5.0f %5.0f %5.0f %5.0f %5.0f %5.0f %5.0f |%5.0f %5.0f %5.0f %5.0f %5.0f %5.0f %5.0f %5.0f |\n",
		c, rate,
		benchmarkRecord.firstByte[avg], benchmarkRecord.firstByte[min], benchmarkRecord.firstByte[p25], benchmarkRecord.firstByte[p50], benchmarkRecord.firstByte[p75], benchmarkRecord.firstByte[p90], benchmarkRecord.firstByte[p99], benchmarkRecord.firstByte[max],
		benchmarkRecord.lastByte[avg], benchmarkRecord.lastByte[min], benchmarkRecord.lastByte[p25], benchmarkRecord.lastByte[p50], benchmarkRecord.lastByte[p75], benchmarkRecord.lastByte[p90], benchmarkRecord.lastByte[p99], benchmarkRecord.lastByte[max])

	// add the results to the csv array
	csvRecords = append(csvRecords, []string{
		fmt.Sprintf("%s", hostname),
		fmt.Sprintf("%d", payloadSize),
		fmt.Sprintf("%d", benchmarkRecord.threads),
		fmt.Sprintf("%.3f", rate),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[avg]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[min]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[p25]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[p50]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[p75]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[p90]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[p99]),
		fmt.Sprintf("%.1f", benchmarkRecord.firstByte[max]),
		fmt.Sprintf("%.2f", benchmarkRecord.lastByte[avg]),
		fmt.Sprintf("%.2f", benchmarkRecord.lastByte[min]),
		fmt.Sprintf("%.1f", benchmarkRecord.lastByte[p25]),
		fmt.Sprintf("%.1f", benchmarkRecord.lastByte[p50]),
		fmt.Sprintf("%.1f", benchmarkRecord.lastByte[p75]),
		fmt.Sprintf("%.1f", benchmarkRecord.lastByte[p90]),
		fmt.Sprintf("%.1f", benchmarkRecord.lastByte[p99]),
		fmt.Sprintf("%.1f", benchmarkRecord.lastByte[max]),
	})

	return csvRecords
}

// prints the table header for the test results
func printHeader(objectSize uint64) {
	// instance type string used to render results to stdout
	instanceTypeString := ""

	// print the table header
	fmt.Printf("Download performance with \033[1;33m%-s\033[0m objects%s\n", byteFormat(float64(objectSize)), instanceTypeString)
	fmt.Println("                           +-------------------------------------------------------------------------------------------------+")
	fmt.Println("                           |            Time to First Byte (ms)             |            Time to Last Byte (ms)              |")
	fmt.Println("+---------+----------------+------------------------------------------------+------------------------------------------------+")
	if !throttlingMode {
		fmt.Println("| Threads |     Throughput |  avg   min   p25   p50   p75   p90   p99   max |  avg   min   p25   p50   p75   p90   p99   max |")
	} else {
		fmt.Println("|       # |     Throughput |  avg   min   p25   p50   p75   p90   p99   max |  avg   min   p25   p50   p75   p90   p99   max |")
	}
	fmt.Println("+---------+----------------+------------------------------------------------+------------------------------------------------+")
}

// generates an S3 key from the sha hash of the hostname, thread index, and object size
func generateS3Key(host string, threadIndex int, payloadSize uint64) string {
	keyHash := sha1.Sum([]byte(fmt.Sprintf("%s-%03d-%012d", host, threadIndex, payloadSize)))
	key := fmt.Sprintf("%x", keyHash)
	return key
}

// cleans up the objects uploaded to S3 for this test (but doesn't remove the bucket)
func cleanup() {
	fmt.Print("\n--- \033[1;32mCLEANUP\033[0m ------------------------------------------------------------------------------------------------------------------\n\n")
	// an object size iterator that starts from 1 KB and doubles the size on every iteration
	generatePayload := payloadSizeGenerator()
	// loop over every payload size
	for p := 1; p <= maxPayload; p++ {
		// get an object size from the iterator
		payloadSize := generatePayload()
		// loop over each possible thread to clean up objects from any previous test execution
		for t := 1; t <= maxThreads; t++ {
			key := generateS3Key(hostname, t, payloadSize)
			_, err := s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
				Bucket: aws.String(bucketName),
				Key:    aws.String(key),
			})

			// if the object doesn't exist, ignore the error
			if err != nil && !strings.HasPrefix(err.Error(), "NotFound: Not Found") {
				panic("Failed to delete object: " + err.Error())
			}
		}
	}
	fmt.Print("\n\n")
}

func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	return hostname
}

// formats bytes to KB or MB
func byteFormat(bytes float64) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.f MB", bytes/1024/1024)
	}
	return fmt.Sprintf("%.f KB", bytes/1024)
}

// returns an object size iterator, starting from 1 KB and double in size by each iteration
func payloadSizeGenerator() func() uint64 {
	nextPayloadSize := uint64(1024)
	return func() uint64 {
		thisPayloadSize := nextPayloadSize
		nextPayloadSize *= 2
		return thisPayloadSize
	}
}

// adjust the sample count for small instances and for low thread counts (so that the test doesn't take forever)
func getTargetSampleCount(threads int, tasks int) int {
	if threads <= 4 {
		return minimumOf(100, tasks)
	}
	if threads <= 8 {
		return minimumOf(250, tasks)
	}
	if threads <= 16 {
		return minimumOf(500, tasks)
	}
	return tasks
}

// go doesn't seem to have a min function in the std lib!
func minimumOf(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// comparator to sort by first byte latency
type ByFirstByte []latency

func (a ByFirstByte) Len() int           { return len(a) }
func (a ByFirstByte) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByFirstByte) Less(i, j int) bool { return a[i].FirstByte < a[j].FirstByte }

// comparator to sort by last byte latency
type ByLastByte []latency

func (a ByLastByte) Len() int           { return len(a) }
func (a ByLastByte) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByLastByte) Less(i, j int) bool { return a[i].LastByte < a[j].LastByte }
