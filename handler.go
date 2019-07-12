package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

type rpcHandler struct {
	paramReceivers []reflect.Type
	nParams        int

	receiver    reflect.Value
	handlerFunc reflect.Value

	hasCtx int

	errOut int
	valOut int
}

type handlers map[string]rpcHandler

// Request / response

type request struct {
	Jsonrpc string  `json:"jsonrpc"`
	ID      *int64  `json:"id,omitempty"`
	Method  string  `json:"method"`
	Params  []param `json:"params"`
}

type respError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *respError) Error() string {
	if e.Code >= -32768 && e.Code <= -32000 {
		return fmt.Sprintf("RPC error (%d): %s", e.Code, e.Message)
	}
	return e.Message
}

type response struct {
	Jsonrpc string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	ID      int64       `json:"id"`
	Error   *respError  `json:"error,omitempty"`
}

// Register

func (h handlers) register(namespace string, r interface{}) {
	val := reflect.ValueOf(r)
	//TODO: expect ptr

	for i := 0; i < val.NumMethod(); i++ {
		method := val.Type().Method(i)

		funcType := method.Func.Type()
		hasCtx := 0
		if funcType.NumIn() >= 2 && funcType.In(1) == contextType {
			hasCtx = 1
		}

		ins := funcType.NumIn() - 1 - hasCtx
		recvs := make([]reflect.Type, ins)
		for i := 0; i < ins; i++ {
			recvs[i] = method.Type.In(i + 1 + hasCtx)
		}

		valOut, errOut, _ := processFuncOut(funcType)

		h[namespace+"."+method.Name] = rpcHandler{
			paramReceivers: recvs,
			nParams:        ins,

			handlerFunc: method.Func,
			receiver:    val,

			hasCtx: hasCtx,

			errOut: errOut,
			valOut: valOut,
		}
	}
}

// Handle

type rpcErrFunc func(w io.Writer, req *request, code int, err error)

func (h handlers) handleReader(ctx context.Context, r io.Reader, w io.Writer, rpcError rpcErrFunc) {
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		rpcError(w, &req, rpcParseError, err)
		return
	}

	h.handle(ctx, req, w, rpcError)
}

func (h handlers) handle(ctx context.Context, req request, w io.Writer, rpcError rpcErrFunc) {
	handler, ok := h[req.Method]
	if !ok {
		rpcError(w, &req, rpcMethodNotFound, fmt.Errorf("method '%s' not found", req.Method))
		return
	}

	if len(req.Params) != handler.nParams {
		rpcError(w, &req, rpcInvalidParams, fmt.Errorf("wrong param count"))
		return
	}

	callParams := make([]reflect.Value, 1+handler.hasCtx+handler.nParams)
	callParams[0] = handler.receiver
	if handler.hasCtx == 1 {
		callParams[1] = reflect.ValueOf(ctx)
	}

	for i := 0; i < handler.nParams; i++ {
		rp := reflect.New(handler.paramReceivers[i])
		if err := json.NewDecoder(bytes.NewReader(req.Params[i].data)).Decode(rp.Interface()); err != nil {
			rpcError(w, &req, rpcParseError, err)
			return
		}

		callParams[i+1+handler.hasCtx] = reflect.ValueOf(rp.Elem().Interface())
	}

	///////////////////

	callResult := handler.handlerFunc.Call(callParams)
	if req.ID == nil {
		return // notification
	}

	///////////////////

	resp := response{
		Jsonrpc: "2.0",
		ID:      *req.ID,
	}

	if handler.errOut != -1 {
		err := callResult[handler.errOut].Interface()
		if err != nil {
			resp.Error = &respError{
				Code:    1,
				Message: err.(error).Error(),
			}
		}
	}
	if handler.valOut != -1 {
		resp.Result = callResult[handler.valOut].Interface()
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Println(err)
		return
	}
}