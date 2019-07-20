# Introduction
s3benchmark is a copy of [s3-benchmark](https://github.com/minio/s3-benchmark) for performing S3 operations (PUT, GET, and DELETE) for objects.
 
# Command Line Arguments
Below are the command line arguments to the program (which can be displayed using -h flag):
```
Usage of ./s3benchmark:
  -a string
    	Access key (default "object_user1")
  -b string
    	Bucket for testing (default "s3benchmark-test")
  -d int
    	Duration of each test in seconds (default 60)
  -e string
    	S3 Endpoint URL (default "http://192.168.55.2:9020")
  -l int
    	Number of times to repeat test (default 1)
  -r string
    	Region for testing (default "cn-north-1")
  -s string
    	Secret key (default "ChangeMeChangeMeChangeMeChangeMeChangeMe")
  -t int
    	Number of threads to run (default 1)
  -z string
    	Size of objects in bytes with postfix K, M, and G (default "128K")
```        

# Example Benchmark
Below is an example run of the benchmark for 10 threads with the default 128K object size.  The benchmark reports
for each operation PUT, GET and DELETE the results in terms of data speed and operations per second.  The program
writes all results to the log file s3benchmark.log.

```
./s3benchmark -t 10 -l 2
s3benchmark v1.1.22-072015
url=http://192.168.55.2:9020, bucket=s3benchmark-test, region=cn-north-1, duration=60, threads=10, loops=2, size=128K(131072)
Loop	Method	  Objects	Elapsed(s)	 Throuphput	   TPS	 Failed
   1	   PUT	    14341	      60.1	     29.9MB	 238.8	      0
   1	   GET	    34581	      60.0	       72MB	 576.3	      0
   1	DELETE	    14351	       5.7	        NaN	2509.2	      0
   2	   PUT	    12164	      60.1	     25.3MB	 202.3	      0
   2	   GET	    31982	      60.0	     66.6MB	 533.0	      0
   2	DELETE	    12174	       5.2	        NaN	2338.8	      0
Benchmark completed.
```
