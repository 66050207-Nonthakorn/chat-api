// Package imagestore keeps processed message images in a private S3 bucket.
package imagestore

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"cloudchat/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Object struct {
	Body io.ReadCloser
	Size int64
}

type S3 struct {
	client *s3.Client
	bucket string
}

func Open(ctx context.Context, cfg config.Config) (*S3, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.ImageRegion)}
	if cfg.ImageEndpoint != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.ImageAccessKey, cfg.ImageSecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load image storage configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.ImageEndpoint != "" {
			options.BaseEndpoint = aws.String(cfg.ImageEndpoint)
			options.UsePathStyle = true
		}
	})
	return &S3{client: client, bucket: cfg.ImageBucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	return err
}

func (s *S3) Get(ctx context.Context, key string) (Object, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return Object{}, err
	}
	if result.ContentLength == nil {
		_ = result.Body.Close()
		return Object{}, fmt.Errorf("image storage omitted object length")
	}
	return Object{Body: result.Body, Size: *result.ContentLength}, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
