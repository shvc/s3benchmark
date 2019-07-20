#!/bin/sh
# Sat Jul 16 22:15:04 CST 2019
#
version="1.1.$(git rev-list HEAD --count)-$(date +'%m%d%H')"
if [ "X$1" = "Xpkg" ]
then
  which zip || { echo 'zip command not find'; exit; }
  echo "Building Linux amd64 s3benchmark-$version"
  GOOS=linux GOARCH=amd64 go build -ldflags " -X main.version=$version"
  zip -m s3benchmark-$version-linux-amd64.zip s3benchmark
  
  echo "Building Macos amd64 s3benchmark-$version"
  GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$version"
  zip -m s3benchmark-$version-macos-amd64.zip s3benchmark
  
  echo "Building Windows amd64 s3benchmark-$version"
  GOOS=windows GOARCH=amd64 go build -ldflags " -X main.version=$version"
  zip -m s3benchmark-$version-win-x64.zip s3benchmark.exe
else
  echo "Building s3benchmark-$version"
  go build -ldflags "-X main.version=$version"
fi