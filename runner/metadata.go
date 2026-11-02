package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

var errInvalidMetadata = errors.New("invalid WebDAV file metadata")

type fileMetadata struct {
	XMLName   xml.Name `xml:"DAV: multistatus"`
	Responses []struct {
		Hrefs    []string `xml:"DAV: href"`
		Statuses []string `xml:"DAV: status"`
		Props    []struct {
			Statuses   []string `xml:"DAV: status"`
			Properties []struct {
				Sizes []string `xml:"DAV: getcontentlength"`
				Types []struct {
					Children []xml.Name `xml:",any"`
					Text     string     `xml:",chardata"`
				} `xml:"DAV: resourcetype"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

// PROPFIND 只读取元数据，避免云盘网关将 HEAD 转交给下载代理。
// 文件身份和属性状态必须同时有效，错误响应中的大小不参与校验。
func (d *DAV) fileSize(ctx context.Context, name string) (int64, error) {
	query := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:resourcetype/></d:prop></d:propfind>`)
	res, err := d.request(ctx, "PROPFIND", name, bytesBody(query), map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	if err != nil {
		return 0, err
	}
	defer closeResponse(res)
	if res.StatusCode != 207 {
		return 0, &checkStatusError{res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return 0, fmt.Errorf("read WebDAV metadata: %w", err)
	}
	invalid := func(reason string) (int64, error) {
		return 0, fmt.Errorf("%w: %s", errInvalidMetadata, reason)
	}
	if len(data) > 1<<20 {
		return invalid("response exceeds limit")
	}
	var result fileMetadata
	decoder := xml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&result) != nil {
		return invalid("malformed XML")
	}
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return invalid("malformed XML suffix")
		}
		if text, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
			return invalid("unexpected XML suffix")
		}
	}
	if len(result.Responses) != 1 {
		return invalid("expected one file response")
	}
	item := result.Responses[0]
	if len(item.Hrefs) != 1 {
		return invalid("missing or duplicate file identity")
	}
	href, e := url.Parse(item.Hrefs[0])
	target := d.target(name)
	if e != nil || item.Hrefs[0] == "" || href.User != nil || href.RawQuery != "" || href.ForceQuery || href.Fragment != "" || (href.Host != "" && (href.Scheme != target.Scheme || href.Host != target.Host)) || (href.Scheme != "" && href.Host == "") {
		return invalid("invalid file identity")
	}
	href = target.ResolveReference(href)
	if href.Path != target.Path || href.Host != target.Host || href.Scheme != target.Scheme {
		return invalid("response belongs to another file")
	}
	if len(item.Statuses) > 0 {
		if len(item.Statuses) == 1 && len(item.Props) == 0 && statusCode(item.Statuses[0]) == 404 {
			return 0, &checkStatusError{404}
		}
		return invalid("unexpected file status")
	}
	var sizes []string
	types := 0
	for _, prop := range item.Props {
		if len(prop.Statuses) != 1 || statusCode(prop.Statuses[0]) == 0 || len(prop.Properties) != 1 {
			return invalid("missing or invalid property status")
		}
		if statusCode(prop.Statuses[0]) != 200 {
			continue
		}
		sizes = append(sizes, prop.Properties[0].Sizes...)
		for _, kind := range prop.Properties[0].Types {
			types++
			if len(kind.Children) != 0 || strings.TrimSpace(kind.Text) != "" {
				return invalid("resource is not a regular file")
			}
		}
	}
	if len(sizes) != 1 || types != 1 {
		return invalid("file size or type unavailable or ambiguous")
	}
	value := strings.TrimSpace(sizes[0])
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return invalid("invalid file size")
	}
	size, e := strconv.ParseInt(value, 10, 64)
	if e != nil {
		return invalid("file size exceeds limit")
	}
	return size, nil
}

func statusCode(value string) int {
	parts := strings.Fields(value)
	if len(parts) < 2 || (parts[0] != "HTTP/1.1" && parts[0] != "HTTP/1.0" && parts[0] != "HTTP/2") {
		return 0
	}
	code, _ := strconv.Atoi(parts[1])
	if code < 100 || code > 599 {
		return 0
	}
	return code
}
