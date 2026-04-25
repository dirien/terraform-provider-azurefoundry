// Copyright (c) Engin Diri
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"path/filepath"
)

// ─────────────────────────────────────────────────────────────────────────────
// File — model types (shared between v1 and v2)
// ─────────────────────────────────────────────────────────────────────────────

type FilePurpose string

const FilePurposeAssistants FilePurpose = "assistants"

type FileResponse struct {
	ID        string      `json:"id"`
	Object    string      `json:"object"`
	Bytes     int64       `json:"bytes"`
	CreatedAt int64       `json:"created_at"`
	Filename  string      `json:"filename"`
	Purpose   FilePurpose `json:"purpose"`
}

type DeleteFileResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// ─────────────────────────────────────────────────────────────────────────────
// File CRUD (classic /files?api-version=APIVersion)
// ─────────────────────────────────────────────────────────────────────────────

// UploadFile uploads file contents with the given filename.
// Use FilePurposeAssistants for files used with file_search or code_interpreter.
func (c *FoundryClient) UploadFile(ctx context.Context, filename string, fileData []byte, purpose FilePurpose) (*FileResponse, error) {
	url := fmt.Sprintf("%s/files?api-version=%s", c.ProjectEndpoint, APIVersion)
	return c.uploadFileMultipart(ctx, url, filename, fileData, purpose)
}

func (c *FoundryClient) GetFile(ctx context.Context, fileID string) (*FileResponse, error) {
	url := fmt.Sprintf("%s/files/%s?api-version=%s", c.ProjectEndpoint, fileID, APIVersion)
	return c.getFile(ctx, url)
}

func (c *FoundryClient) DeleteFile(ctx context.Context, fileID string) (*DeleteFileResponse, error) {
	url := fmt.Sprintf("%s/files/%s?api-version=%s", c.ProjectEndpoint, fileID, APIVersion)
	return c.deleteFile(ctx, url)
}

// ─────────────────────────────────────────────────────────────────────────────
// File CRUD (v2 /files?api-version=v1)
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) UploadFileV2(ctx context.Context, filename string, fileData []byte, purpose FilePurpose) (*FileResponse, error) {
	url := c.ProjectEndpoint + "/files?api-version=v1"
	return c.uploadFileMultipart(ctx, url, filename, fileData, purpose)
}

func (c *FoundryClient) GetFileV2(ctx context.Context, fileID string) (*FileResponse, error) {
	url := fmt.Sprintf("%s/files/%s?api-version=v1", c.ProjectEndpoint, fileID)
	return c.getFile(ctx, url)
}

func (c *FoundryClient) DeleteFileV2(ctx context.Context, fileID string) (*DeleteFileResponse, error) {
	url := fmt.Sprintf("%s/files/%s?api-version=v1", c.ProjectEndpoint, fileID)
	return c.deleteFile(ctx, url)
}

// ─────────────────────────────────────────────────────────────────────────────
// File helpers — shared between v1 and v2
// ─────────────────────────────────────────────────────────────────────────────

func (c *FoundryClient) uploadFileMultipart(ctx context.Context, url, filename string, fileData []byte, purpose FilePurpose) (*FileResponse, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("purpose", string(purpose)); err != nil {
		return nil, fmt.Errorf("writing purpose field: %w", err)
	}

	part, err := mw.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return nil, fmt.Errorf("creating form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("writing file data: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", err)
	}

	httpReq, err := c.newRequestRaw(ctx, http.MethodPost, url, &buf, mw.FormDataContentType())
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upload file HTTP error: %w", err)
	}
	defer closeBody(resp)

	if err := checkResponseError(resp); err != nil {
		return nil, err
	}

	var result FileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding upload file response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) getFile(ctx context.Context, url string) (*FileResponse, error) {
	httpReq, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get file HTTP error: %w", err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result FileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding get file response: %w", err)
	}
	return &result, nil
}

func (c *FoundryClient) deleteFile(ctx context.Context, url string) (*DeleteFileResponse, error) {
	httpReq, err := c.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("delete file HTTP error: %w", err)
	}
	defer closeBody(resp)
	if err := checkResponseError(resp); err != nil {
		return nil, err
	}
	var result DeleteFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding delete file response: %w", err)
	}
	return &result, nil
}
