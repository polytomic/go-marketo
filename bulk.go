package marketo

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

type ImportObject struct {
	create   string
	status   string
	failures string
}

var (
	Leads = ImportObject{
		create:   "leads",
		status:   "leads/batch/%d",
		failures: "leads/batch/%d/failures",
	}
	importObjects = map[string]ImportObject{
		"lead": Leads,
	}
)

// ImportObjectForAPIName returns the ImportObject given the API name
// of a Marketo object
func ImportObjectForAPIName(apiName string) ImportObject {
	if obj, ok := importObjects[apiName]; ok {
		return obj
	}

	return ImportObject{
		create:   fmt.Sprintf("customobjects/%s/import", apiName),
		status:   fmt.Sprintf("customobjects/%s/import/%%d/status", apiName),
		failures: fmt.Sprintf("customobjects/%s/import/%%d/failures", apiName),
	}
}

const (
	BatchComplete  = "Complete"
	BatchQueued    = "Queued"
	BatchImporting = "Importing"
	BatchFailed    = "Failed"
)

const (
	createImport      = "create bulk import"
	getImport         = "get import status"
	getImportFailures = "get import failures"
)

// BatchResult contains the details of a batch, returned by the Create
// & Get functions
type BatchResult struct {
	BatchID          int    `json:"batchId"`
	ImportID         string `json:"importId"`
	Status           string `json:"status"`
	LeadsProcessed   int    `json:"numOfLeadsProcessed,omitempty"`
	Failures         int    `json:"numOfRowsFailed"`
	Warnings         int    `json:"numOfRowsWithWarning"`
	Message          string `json:"message"`
	ObjectsProcessed int    `json:"numOfObjectsProcessed,omitempty"`
	ObjectName       string `json:"objectApiName,omitempty"`

	Processed int `json:"-"`
}

// ImportAPI provides access to the Marketo import API
type ImportAPI struct {
	*Client
}

// NewImportAPI returns a new instance of the import API, configured
// using the provided options
func NewImportAPI(c *Client) *ImportAPI {
	return &ImportAPI{c}
}

// Create uploads a new file for importing, returning the new
// asynchronous import
func (i *ImportAPI) Create(ctx context.Context, obj ImportObject, file io.Reader) ([]BatchResult, error) {
	buffer := &strings.Builder{}
	mpWriter := multipart.NewWriter(buffer)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename="%s"`, "import.csv"))

	fileWriter, err := mpWriter.CreatePart(h)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(fileWriter, file)
	if err != nil {
		return nil, err
	}

	mpWriter.Close()
	request, err := http.NewRequest(http.MethodPost,
		i.url("bulk", "v1", fmt.Sprintf("%s.json?format=csv", obj.create)),
		bytes.NewBufferString(buffer.String()),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Add("Content-Type", mpWriter.FormDataContentType())

	resp, err := i.Client.doRequest(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, handleError(createImport, resp)
	}

	response := &Response{}
	reader := json.NewDecoder(resp.Body)
	err = reader.Decode(response)
	if err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		return nil, ErrorForReasons(resp.StatusCode, response.Errors...)
	}

	results := []BatchResult{}
	err = json.Unmarshal(response.Result, &results)
	if err != nil {
		return nil, err
	}

	return results, nil
}

// Get retrieves an existing import by its batch ID
func (i *ImportAPI) Get(ctx context.Context, obj ImportObject, id int) (*BatchResult, error) {
	request, err := http.NewRequest(
		http.MethodGet, i.url("bulk", "v1", fmt.Sprintf("%s.json",
			fmt.Sprintf(obj.status, id),
		)), nil,
	)
	if err != nil {
		return nil, err
	}

	resp, err := i.Client.doRequest(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, handleError(getImport, resp)
	}

	response := &Response{}
	reader := json.NewDecoder(resp.Body)
	err = reader.Decode(response)
	if err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		return nil, ErrorForReasons(resp.StatusCode, response.Errors...)
	}

	result := []BatchResult{}
	err = json.Unmarshal(response.Result, &result)
	if err != nil {
		return nil, err
	}
	if len(result) < 1 {
		return nil, errors.New("not found")
	}

	for i, r := range result {
		result[i].Processed = r.ObjectsProcessed
		if r.LeadsProcessed > 0 {
			result[i].Processed = r.LeadsProcessed
		}
	}
	return &result[0], nil
}

// LeadImportFailure contains a single lead record failure, along with
// the reason for failure.
type LeadImportFailure struct {
	Reason string
	Fields map[string]interface{}
}

// Failures returns the list of failed recrods for an import
func (i *ImportAPI) Failures(ctx context.Context, obj ImportObject, id int) ([]LeadImportFailure, error) {
	request, err := http.NewRequest(
		http.MethodGet, i.url("bulk", "v1", fmt.Sprintf("%s.json",
			fmt.Sprintf(obj.failures, id),
		)), nil,
	)
	if err != nil {
		return nil, err
	}

	resp, err := i.Client.doRequest(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// no errors
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, handleError(getImportFailures, resp)
	}

	reader := csv.NewReader(resp.Body)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}

	failures := []LeadImportFailure{}
	record, err := reader.Read()
	for err == nil {
		failure := LeadImportFailure{
			Reason: record[len(header)-1],
			Fields: map[string]interface{}{},
		}
		for i := 0; i < len(header)-1; i++ {
			failure.Fields[header[i]] = record[i]
		}
		failures = append(failures, failure)
		record, err = reader.Read()
	}
	return failures, nil
}
