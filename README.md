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
Below is an example run of the benchmark for 320 threads with the 128K object size.
The benchmark reports for each operation PUT, GET and DELETE the results in terms of data speed and operations per second.
The program writes all results to the log file s3benchmark.log.
```
./s3benchmark -e http://172.16.3.55:9020 -t 320 -l 3
s3benchmark vpn201 v1.1.5-072116
url=http://172.16.3.55:9020, bucket=s3benchmark-test, region=cn-north-1, duration=60, threads=320, loops=3, size=128K(131072)
2019/07/21 16:30:42 WARNING: Bucket:s3benchmark-test already exists
Loop	Method	  Objects	Elapsed(s)	 Throuphput	   TPS	 Failed
   1	   PUT	    10506	      61.2	     21.4MB	 171.5	      0
   1	   GET	    19173	      61.0	     39.3MB	 314.3	      0
   1	DELETE	    10826	       4.9	         --	2154.3	      0
   2	   PUT	     9389	      61.0	     19.2MB	 153.8	      0
   2	   GET	    19200	      60.8	     39.5MB	 315.7	      0
   2	DELETE	     9709	       4.7	         --	2017.6	      0
   3	   PUT	     9779	      60.9	     20.1MB	 160.4	      0
   3	   GET	    19200	      60.8	     39.5MB	 315.7	      0
   3	DELETE	    10099	       2.0	         --	4780.9	      0
 AVG	   PUT	    29674	     183.2	     20.2MB	 161.9	      0
 AVG	   GET	    57573	     182.6	     39.4MB	 315.2	      0
 AVG	DELETE	    30634	      11.6	         --	2646.4	      0
```
