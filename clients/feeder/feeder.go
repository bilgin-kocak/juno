package feeder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NethermindEth/juno/core/felt"
	"github.com/NethermindEth/juno/starknet"
	"github.com/NethermindEth/juno/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ErrDeprecatedCompiledClass = errors.New("deprecated compiled class")

type Backoff func(wait time.Duration) time.Duration

type Client struct {
	readURL    string
	writeURL   string
	client     *http.Client
	backoff    Backoff
	maxRetries int
	maxWait    time.Duration
	minWait    time.Duration
	log        utils.SimpleLogger
	userAgent  string
	apiKey     string
	listener   EventListener
}

func (c *Client) WithListener(l EventListener) *Client {
	c.listener = l
	return c
}

func (c *Client) WithBackoff(b Backoff) *Client {
	c.backoff = b
	return c
}

func (c *Client) WithMaxRetries(num int) *Client {
	c.maxRetries = num
	return c
}

func (c *Client) WithMaxWait(d time.Duration) *Client {
	c.maxWait = d
	return c
}

func (c *Client) WithMinWait(d time.Duration) *Client {
	c.minWait = d
	return c
}

func (c *Client) WithLogger(log utils.SimpleLogger) *Client {
	c.log = log
	return c
}

func (c *Client) WithUserAgent(ua string) *Client {
	c.userAgent = ua
	return c
}

func (c *Client) WithTimeout(t time.Duration) *Client {
	c.client.Timeout = t
	return c
}

func (c *Client) WithAPIKey(key string) *Client {
	c.apiKey = key
	return c
}

func ExponentialBackoff(wait time.Duration) time.Duration {
	return wait * 2
}

func NopBackoff(d time.Duration) time.Duration {
	return 0
}

// NewTestClient returns a client and a function to close a test server.
func NewTestClient(t *testing.T, network utils.Network) *Client {
	srvRead := newReadTestServer(t, network)
	t.Cleanup(srvRead.Close)
	srvWrite := newWriteTestServer(t)
	t.Cleanup(srvWrite.Close)
	ua := "Juno/v0.0.1-test Starknet Implementation"
	apiKey := "API_KEY"

	c := NewClient(srvRead.URL, srvWrite.URL).WithBackoff(NopBackoff).WithMaxRetries(0).WithUserAgent(ua).WithAPIKey(apiKey)
	c.client = &http.Client{
		Transport: &http.Transport{
			// On macOS tests often fail with the following error:
			//
			// "Get "http://127.0.0.1:xxxx/get_{feeder gateway method}?{arg}={value}": dial tcp 127.0.0.1:xxxx:
			//    connect: can't assign requested address"
			//
			// This error makes running local tests, in quick succession, difficult because we have to wait for the OS to release ports.
			// Sometimes the sync tests will hang because sync process will keep making requests if there was some error.
			// This problem is further exacerbated by having parallel tests.
			//
			// Increasing test client's idle conns allows for large concurrent requests to be made from a single test client.
			MaxIdleConnsPerHost: 1000,
		},
	}
	return c
}

func newWriteTestServer(t *testing.T) *httptest.Server {
	// As this is a test sever we mimic the response for one good and one bad request.
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, []string{"API_KEY"}, r.Header["X-Throttling-Bypass"])
		assert.Equal(t, []string{"Juno/v0.0.1-test Starknet Implementation"}, r.Header["User-Agent"])

		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error())) //nolint:errcheck
			return
		}

		// empty request: "{}"
		emptyReqLen := 4
		if string(b) == "null" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		} else if len(b) <= emptyReqLen {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code": "Malformed Request", "message": "empty request"}`)) //nolint:errcheck
			return
		}

		hash := new(felt.Felt).SetBytes([]byte("random"))
		resp := fmt.Sprintf("{\"code\": \"TRANSACTION_RECEIVED\", \"transaction_hash\": %q, \"address\": %q}", hash.String(), hash.String())
		w.Write([]byte(resp)) //nolint:errcheck
	}))
}

func newReadTestServer(t *testing.T, network utils.Network) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryMap, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		assert.Equal(t, []string{"API_KEY"}, r.Header["X-Throttling-Bypass"])
		assert.Equal(t, []string{"Juno/v0.0.1-test Starknet Implementation"}, r.Header["User-Agent"])

		wd, err := os.Getwd()
		require.NoError(t, err)

		base := wd[:strings.LastIndex(wd, "juno")+4]
		queryArg := ""
		dir := ""
		const blockNumberArg = "blockNumber"
		switch {
		case strings.HasSuffix(r.URL.Path, "get_block"):
			dir = "block"
			queryArg = blockNumberArg
		case strings.HasSuffix(r.URL.Path, "get_state_update"):
			queryArg = blockNumberArg
			if includeBlock, ok := queryMap["includeBlock"]; !ok || len(includeBlock) == 0 {
				dir = "state_update"
			} else {
				dir = "state_update_with_block"
			}
		case strings.HasSuffix(r.URL.Path, "get_transaction"):
			dir = "transaction"
			queryArg = "transactionHash"
		case strings.HasSuffix(r.URL.Path, "get_class_by_hash"):
			dir = "class"
			queryArg = "classHash"
		case strings.HasSuffix(r.URL.Path, "get_compiled_class_by_class_hash"):
			dir = "compiled_class"
			queryArg = "classHash"
		case strings.HasSuffix(r.URL.Path, "get_public_key"):
			dir = "public_key"
			queryArg = "pk"
			queryMap[queryArg] = []string{queryArg}
		case strings.HasSuffix(r.URL.Path, "get_signature"):
			dir = "signature"
			queryArg = blockNumberArg
		case strings.HasSuffix(r.URL.Path, "get_block_traces"):
			dir = "traces"
			queryArg = "blockHash"
		}

		fileName, found := queryMap[queryArg]
		if !found {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		path := filepath.Join(base, "clients", "feeder", "testdata", network.String(), dir, fileName[0]+".json")
		read, err := os.ReadFile(path)
		if err != nil {
			handleNotFound(dir, queryArg, w)
			return
		}
		w.Write(read) //nolint:errcheck
	}))
}

func handleNotFound(dir, queryArg string, w http.ResponseWriter) {
	// If a transaction data is missing, respond with
	// {"finality_status": "NOT_RECEIVED", "status": "NOT_RECEIVED"}
	// instead of 404 as per real test server behaviour.
	if dir == "transaction" && queryArg == "transactionHash" {
		w.Write([]byte("{\"finality_status\": \"NOT_RECEIVED\", \"status\": \"NOT_RECEIVED\"}")) //nolint:errcheck
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
}

func NewClient(readURL, writeURL string) *Client {
	return &Client{
		readURL:  strings.TrimSuffix(readURL, "/") + "/",
		writeURL: strings.TrimSuffix(writeURL, "/") + "/",
		client: &http.Client{
			Timeout: time.Minute,
		},
		backoff:    ExponentialBackoff,
		maxRetries: 10, // ~40 secs with default backoff and maxWait (block time on mainnet is 20 seconds on average)
		maxWait:    4 * time.Second,
		minWait:    time.Second,
		log:        utils.NewNopZapLogger(),
		listener:   &SelectiveListener{},
	}
}

// buildQueryString builds the query url with encoded parameters
func (c *Client) buildQueryString(endpoint string, args map[string]string) string {
	base, err := url.Parse(c.readURL)
	if err != nil {
		panic("Malformed feeder base URL")
	}

	base.Path += endpoint

	params := url.Values{}
	for k, v := range args {
		params.Add(k, v)
	}
	base.RawQuery = params.Encode()

	return base.String()
}

// GET methods.

// get performs a "GET" http request with the given URL and returns the response body
func (c *Client) get(ctx context.Context, queryURL string) (io.ReadCloser, error) {
	var res *http.Response
	var err error
	wait := time.Duration(0)
	for i := 0; i <= c.maxRetries; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
			var req *http.Request
			req, err = http.NewRequestWithContext(ctx, "GET", queryURL, http.NoBody)
			if err != nil {
				return nil, err
			}
			if c.userAgent != "" {
				req.Header.Set("User-Agent", c.userAgent)
			}
			if c.apiKey != "" {
				req.Header.Set("X-Throttling-Bypass", c.apiKey)
			}

			reqTimer := time.Now()
			res, err = c.client.Do(req)
			if err == nil {
				c.listener.OnResponse(req.URL.Path, res.StatusCode, time.Since(reqTimer))
				if res.StatusCode == http.StatusOK {
					return res.Body, nil
				} else {
					err = errors.New(res.Status)
				}

				res.Body.Close()
			}

			if wait < c.minWait {
				wait = c.minWait
			}
			wait = c.backoff(wait)
			if wait > c.maxWait {
				wait = c.maxWait
			}
			c.log.Debugw("Failed query to feeder, retrying...", "req", req.URL.String(), "retryAfter", wait.String(), "err", err)
		}
	}
	return nil, err
}

func (c *Client) StateUpdate(ctx context.Context, blockID string) (*starknet.StateUpdate, error) {
	queryURL := c.buildQueryString("get_state_update", map[string]string{
		"blockNumber": blockID,
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	update := new(starknet.StateUpdate)
	if err = json.NewDecoder(body).Decode(update); err != nil {
		return nil, err
	}
	return update, nil
}

func (c *Client) Transaction(ctx context.Context, transactionHash *felt.Felt) (*starknet.TransactionStatus, error) {
	queryURL := c.buildQueryString("get_transaction", map[string]string{
		"transactionHash": transactionHash.String(),
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	txStatus := new(starknet.TransactionStatus)
	if err = json.NewDecoder(body).Decode(txStatus); err != nil {
		return nil, err
	}
	return txStatus, nil
}

func (c *Client) Block(ctx context.Context, blockID string) (*starknet.Block, error) {
	queryURL := c.buildQueryString("get_block", map[string]string{
		"blockNumber": blockID,
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	block := new(starknet.Block)
	if err = json.NewDecoder(body).Decode(block); err != nil {
		return nil, err
	}
	return block, nil
}

func (c *Client) ClassDefinition(ctx context.Context, classHash *felt.Felt) (*starknet.ClassDefinition, error) {
	queryURL := c.buildQueryString("get_class_by_hash", map[string]string{
		"classHash": classHash.String(),
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	class := new(starknet.ClassDefinition)
	if err = json.NewDecoder(body).Decode(class); err != nil {
		return nil, err
	}
	return class, nil
}

func (c *Client) CompiledClassDefinition(ctx context.Context, classHash *felt.Felt) (*starknet.CompiledClass, error) {
	queryURL := c.buildQueryString("get_compiled_class_by_class_hash", map[string]string{
		"classHash": classHash.String(),
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	definition, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}

	if deprecated, _ := starknet.IsDeprecatedCompiledClassDefinition(definition); deprecated {
		return nil, ErrDeprecatedCompiledClass
	}

	class := new(starknet.CompiledClass)
	if err = json.Unmarshal(definition, class); err != nil {
		return nil, err
	}
	return class, nil
}

func (c *Client) PublicKey(ctx context.Context) (*felt.Felt, error) {
	queryURL := c.buildQueryString("get_public_key", nil)

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	publicKey := new(felt.Felt).SetBytes(b)

	return publicKey, nil
}

func (c *Client) Signature(ctx context.Context, blockID string) (*starknet.Signature, error) {
	queryURL := c.buildQueryString("get_signature", map[string]string{
		"blockNumber": blockID,
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	signature := new(starknet.Signature)
	if err := json.NewDecoder(body).Decode(signature); err != nil {
		return nil, err
	}

	return signature, nil
}

func (c *Client) StateUpdateWithBlock(ctx context.Context, blockID string) (*starknet.StateUpdateWithBlock, error) {
	queryURL := c.buildQueryString("get_state_update", map[string]string{
		"blockNumber":  blockID,
		"includeBlock": "true",
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	stateUpdate := new(starknet.StateUpdateWithBlock)
	if err := json.NewDecoder(body).Decode(stateUpdate); err != nil {
		return nil, err
	}

	return stateUpdate, nil
}

func (c *Client) BlockTrace(ctx context.Context, blockHash string) (*starknet.BlockTrace, error) {
	queryURL := c.buildQueryString("get_block_traces", map[string]string{
		"blockHash": blockHash,
	})

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	traces := new(starknet.BlockTrace)
	if err = json.NewDecoder(body).Decode(traces); err != nil {
		return nil, err
	}
	return traces, nil
}

// POST methods.

type ErrorCode string

type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e Error) Error() string {
	return e.Message
}

var (
	InvalidContractClass            ErrorCode = "StarknetErrorCode.INVALID_CONTRACT_CLASS"
	UndeclaredClass                 ErrorCode = "StarknetErrorCode.UNDECLARED_CLASS"
	ClassAlreadyDeclared            ErrorCode = "StarknetErrorCode.CLASS_ALREADY_DECLARED"
	InsufficientMaxFee              ErrorCode = "StarknetErrorCode.INSUFFICIENT_MAX_FEE"
	InsufficientAccountBalance      ErrorCode = "StarknetErrorCode.INSUFFICIENT_ACCOUNT_BALANCE"
	ValidateFailure                 ErrorCode = "StarknetErrorCode.VALIDATE_FAILURE"
	ContractBytecodeSizeTooLarge    ErrorCode = "StarknetErrorCode.CONTRACT_BYTECODE_SIZE_TOO_LARGE"
	DuplicatedTransaction           ErrorCode = "StarknetErrorCode.DUPLICATED_TRANSACTION"
	InvalidTransactionNonce         ErrorCode = "StarknetErrorCode.INVALID_TRANSACTION_NONCE"
	CompilationFailed               ErrorCode = "StarknetErrorCode.COMPILATION_FAILED"
	InvalidCompiledClassHash        ErrorCode = "StarknetErrorCode.INVALID_COMPILED_CLASS_HASH"
	ContractClassObjectSizeTooLarge ErrorCode = "StarknetErrorCode.CONTRACT_CLASS_OBJECT_SIZE_TOO_LARGE"
	InvalidTransactionVersion       ErrorCode = "StarknetErrorCode.INVALID_TRANSACTION_VERSION"
	InvalidContractClassVersion     ErrorCode = "StarknetErrorCode.INVALID_CONTRACT_CLASS_VERSION"
)

// post performs additional utility function over doPost method
func (c *Client) post(ctx context.Context, queryURL string, data any) ([]byte, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", queryURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.apiKey != "" {
		req.Header.Set("X-Throttling-Bypass", c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var gatewayError Error
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil && len(body) > 0 {
			if err := json.Unmarshal(body, &gatewayError); err == nil {
				if len(gatewayError.Code) != 0 {
					return nil, &gatewayError
				}
			}
			return nil, errors.New(string(body))
		}
		return nil, errors.New(resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func (c *Client) AddTransaction(ctx context.Context, txn json.RawMessage) (json.RawMessage, error) {
	return c.post(ctx, c.writeURL+"add_transaction", txn)
}
