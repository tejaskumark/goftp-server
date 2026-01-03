// Copyright 2020 The goftp Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package minio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/tejaskumark/goftp-server"
)

var _ server.Driver = &Driver{}

// Driver implements Driver to store files in minio
type Driver struct {
	client *minio.Client
	bucket string
}

// NewDriver implements DriverFactory
func NewDriver(endpoint, accessKeyID, secretAccessKey, location, bucket string, useSSL bool) (server.Driver, error) {
	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	if err = minioClient.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: location}); err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucket)
		if !exists || errBucketExists != nil {
			return nil, err
		}
	}

	return &Driver{
		client: minioClient,
		bucket: bucket,
	}, nil
}

func buildMinioPath(p string) string {
	return strings.TrimPrefix(p, "/")
}

func buildMinioDir(p string) string {
	v := buildMinioPath(p)
	if !strings.HasSuffix(v, "/") {
		return v + "/"
	}
	return v
}

type minioFileInfo struct {
	p     string
	info  minio.ObjectInfo
	isDir bool
}

func (m *minioFileInfo) Name() string {
	return m.p
}

func (m *minioFileInfo) Size() int64 {
	return m.info.Size
}

func (m *minioFileInfo) Mode() os.FileMode {
	return os.ModePerm
}

func (m *minioFileInfo) ModTime() time.Time {
	return m.info.LastModified
}

func (m *minioFileInfo) IsDir() bool {
	return m.isDir
}

func (m *minioFileInfo) Sys() interface{} {
	return nil
}

func (driver *Driver) isDir(ctx context.Context, path string) (bool, error) {
	p := buildMinioDir(path)

	info, err := driver.client.StatObject(ctx, driver.bucket, p, minio.StatObjectOptions{})
	if err != nil {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		objectCh := driver.client.ListObjects(ctx, driver.bucket, minio.ListObjectsOptions{
			Prefix:    p,
			Recursive: false,
		})
		for object := range objectCh {
			if strings.HasPrefix(object.Key, p) {
				return true, nil
			}
		}

		return false, nil
	}

	return strings.HasSuffix(info.Key, "/"), nil
}

// Stat implements Driver
func (driver *Driver) Stat(ctx *server.Context, path string) (os.FileInfo, error) {
	if path == "/" {
		return &minioFileInfo{
			p:     "/",
			isDir: true,
		}, nil
	}

	p := buildMinioPath(path)
	objInfo, err := driver.client.StatObject(ctx, driver.bucket, p, minio.StatObjectOptions{})
	if err != nil {
		if isDir, err := driver.isDir(ctx, p); err != nil {
			return nil, err
		} else if isDir {
			return &minioFileInfo{
				p:     path,
				isDir: true,
			}, nil
		}
		return nil, errors.New("Not a directory")
	}
	isDir := strings.HasSuffix(objInfo.Key, "/")
	return &minioFileInfo{
		p:     p,
		info:  objInfo,
		isDir: isDir,
	}, nil
}

// ListDir implements Driver
func (driver *Driver) ListDir(ctx *server.Context, path string, callback func(os.FileInfo) error) error {
	p := buildMinioDir(path)
	if p == "/" {
		p = ""
	}
	objectCh := driver.client.ListObjects(ctx, driver.bucket, minio.ListObjectsOptions{
		Prefix:    p,
		Recursive: false,
	})
	for object := range objectCh {
		if object.Err != nil {
			return object.Err
		}

		// ignore itself
		if object.Key == p {
			continue
		}

		isDir := strings.HasSuffix(object.Key, "/")
		info := minioFileInfo{
			p:     strings.TrimPrefix(object.Key, p),
			info:  object,
			isDir: isDir,
		}

		if err := callback(&info); err != nil {
			return err
		}
	}

	return nil
}

// DeleteDir implements Driver
func (driver *Driver) DeleteDir(ctx *server.Context, path string) error {
	p := buildMinioPath(path)
	objectCh := driver.client.ListObjects(ctx, driver.bucket, minio.ListObjectsOptions{
		Prefix:    p,
		Recursive: true,
	})
	for object := range objectCh {
		if object.Err != nil {
			return object.Err
		}

		if err := driver.client.RemoveObject(ctx, driver.bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteFile implements Driver
func (driver *Driver) DeleteFile(ctx *server.Context, path string) error {
	return driver.client.RemoveObject(ctx, driver.bucket, buildMinioPath(path), minio.RemoveObjectOptions{})
}

// Rename implements Driver
func (driver *Driver) Rename(ctx *server.Context, fromPath string, toPath string) error {
	src := minio.CopySrcOptions{Bucket: driver.bucket, Object: buildMinioPath(fromPath)}
	dst := minio.CopyDestOptions{Bucket: driver.bucket, Object: buildMinioPath(toPath)}

	if _, err := driver.client.CopyObject(ctx, dst, src); err != nil {
		return err
	}

	return driver.client.RemoveObject(ctx, driver.bucket, buildMinioPath(fromPath), minio.RemoveObjectOptions{})
}

// MakeDir implements Driver
func (driver *Driver) MakeDir(ctx *server.Context, path string) error {
	dirPath := buildMinioDir(path)
	_, err := driver.client.PutObject(ctx, driver.bucket, dirPath, nil, 0, minio.PutObjectOptions{})
	return err
}

// GetFile implements Driver
func (driver *Driver) GetFile(ctx *server.Context, path string, offset int64) (int64, io.ReadCloser, error) {
	opts := minio.GetObjectOptions{}
	object, err := driver.client.GetObject(ctx, driver.bucket, buildMinioPath(path), opts)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if err != nil && object != nil {
			object.Close()
		}
	}()
	_, err = object.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, nil, err
	}

	info, err := object.Stat()
	if err != nil {
		return 0, nil, err
	}

	return info.Size - offset, object, nil
}

// PutFile implements Driver
func (driver *Driver) PutFile(ctx *server.Context, destPath string, data io.Reader, offset int64) (int64, error) {
	p := buildMinioPath(destPath)
	if offset == -1 {
		uploadInfo, err := driver.client.PutObject(ctx, driver.bucket, p, data, -1, minio.PutObjectOptions{ContentType: "application/octet-stream"})
		if err != nil {
			return 0, err
		}
		return uploadInfo.Size, nil
	}

	tempFile := p + ".tmp"
	defer func() {
		if err := driver.DeleteFile(ctx, tempFile); err != nil {
			log.Println(err)
		}
	}()

	info, err := driver.client.StatObject(ctx, driver.bucket, p, minio.StatObjectOptions{})
	if err != nil {
		return 0, err
	}
	if offset != info.Size {
		return 0, fmt.Errorf("It's unsupported that offset %d is not equal to %d", offset, info.Size)
	}

	uploadInfo, err := driver.client.PutObject(ctx, driver.bucket, tempFile, data, -1, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return 0, err
	}

	srcs := []minio.CopySrcOptions{
		{Bucket: driver.bucket, Object: tempFile},
		{Bucket: driver.bucket, Object: p},
	}
	dst := minio.CopyDestOptions{Bucket: driver.bucket, Object: p}

	uploadInfo, err = driver.client.ComposeObject(ctx, dst, srcs...)
	if err != nil {
		return 0, err
	}
	return uploadInfo.Size, nil
}
