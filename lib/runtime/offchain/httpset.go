package offchain

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"time"
)

type HTTPError int

func (e HTTPError) Index() uint {
	return uint(e)
}

const maxConcurrentRequests = 1000

var (
	errIntBufferEmpty        = errors.New("int buffer exhausted")
	errIntBufferFull         = errors.New("int buffer is full")
	errRequestIDNotAvailable = errors.New("request id not available")
	errInvalidRequest        = errors.New("request is invalid")
	errRequestAlreadyStarted = errors.New("request has already started")
	errInvalidHeaderKey      = errors.New("invalid header key")

	ErrTimeoutWriteBody = errors.New("deadline reach while writing request body")

	// HTTP error is a varying data type
	HTTPErrorDeadlineReached HTTPError = 0
	HTTPErrorIO              HTTPError = 1
	HTTPErrorInvalidID       HTTPError = 2
)

// requestIDBuffer created to control the amount of available non-duplicated ids
type requestIDBuffer chan int16

// newIntBuffer creates the request id buffer starting from 1 till @buffSize (by default @buffSize is 1000)
func newIntBuffer(buffSize int16) requestIDBuffer {
	b := make(chan int16, buffSize)
	for i := int16(1); i <= buffSize; i++ {
		b <- i
	}

	return b
}

func (b requestIDBuffer) get() (int16, error) {
	select {
	case v := <-b:
		return v, nil
	default:
		return 0, errIntBufferEmpty
	}
}

func (b requestIDBuffer) put(i int16) error {
	select {
	case b <- i:
		return nil
	default:
		return errIntBufferFull
	}
}

type OffchainRequest struct {
	Request          *http.Request
	invalid, waiting bool
}

// AddHeader add a new header into @req property only if request is valid or has not started yet
func (r *OffchainRequest) AddHeader(k, v string) error {
	if r.invalid {
		return errInvalidRequest
	}

	if r.waiting {
		return errRequestAlreadyStarted
	}

	if k == "" {
		return fmt.Errorf("%w: %s", errInvalidHeaderKey, "empty header key")
	}

	r.Request.Header.Add(k, v)
	return nil
}

func (r *OffchainRequest) WriteBody(data []byte, deadline *int64) error {
	writeDone := make(chan error)
	defer close(writeDone)

	go func() {
		currBody, err := io.ReadAll(r.Request.Body)
		defer r.Request.Body.Close()
		if err != nil {
			writeDone <- err
			return
		}

		currBodyBuff := bytes.NewBuffer(currBody)
		amountWrite, err := currBodyBuff.Write(data)
		if err != nil {
			writeDone <- err
			return
		}

		if amountWrite != len(data) {
			writeDone <- fmt.Errorf("total chunk length: %v, total chunk write: %v", len(data), amountWrite)
			return
		}

		r.Request.Body = ioutil.NopCloser(currBodyBuff)
		r.Request.ContentLength = int64(currBodyBuff.Len())
		writeDone <- nil
	}()

	if deadline == nil {
		// deadline was passed as None then blocks indefinitely
		err := <-writeDone
		return err
	}

	select {
	case err := <-writeDone:
		return err
	case <-time.After(time.Duration(*deadline)):
		return errTimeoutWriteBody
	}
}

func (r *OffchainRequest) IsValid() bool {
	return !r.invalid
}

// HTTPSet holds a pool of concurrent http request calls
type HTTPSet struct {
	mtx    *sync.Mutex
	reqs   map[int16]*OffchainRequest
	idBuff requestIDBuffer
}

// NewHTTPSet creates a offchain http set that can be used
// by runtime as HTTP clients, the max concurrent requests is 1000
func NewHTTPSet() *HTTPSet {
	return &HTTPSet{
		mtx:    new(sync.Mutex),
		reqs:   make(map[int16]*OffchainRequest),
		idBuff: newIntBuffer(maxConcurrentRequests),
	}
}

// StartRequest create a new request using the method and the uri, adds the request into the list
// and then return the position of the request inside the list
func (p *HTTPSet) StartRequest(method, uri string) (int16, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	id, err := p.idBuff.get()
	if err != nil {
		return 0, err
	}

	if _, ok := p.reqs[id]; ok {
		return 0, errRequestIDNotAvailable
	}

	req, err := http.NewRequest(method, uri, nil)
	if err != nil {
		return 0, err
	}

	p.reqs[id] = &OffchainRequest{
		Request: req,
		invalid: false,
		waiting: false,
	}

	return id, nil
}

// Remove just remove a expecific request from reqs
func (p *HTTPSet) Remove(id int16) error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	delete(p.reqs, id)

	return p.idBuff.put(id)
}

// Get returns a request or nil if request not found
func (p *HTTPSet) Get(id int16) *OffchainRequest {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	return p.reqs[id]
}
