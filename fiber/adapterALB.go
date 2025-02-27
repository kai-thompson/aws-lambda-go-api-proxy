// Package fiberadapter adds Fiber support for the aws-severless-go-api library.
// Uses the core package behind the scenes and exposes the New method to
// get a new instance and Proxy method to send request to the Fiber app.
package fiberadapter

import (
	"context"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/valyala/fasthttp"

	"github.com/awslabs/aws-lambda-go-api-proxy/core"
)

// FiberLambda makes it easy to send API Gateway proxy events to a fiber.App.
// The library transforms the proxy event into an HTTP request and then
// creates a proxy response object from the *fiber.Ctx
type FiberLambdaALB struct {
	core.RequestAccessorALB
	app *fiber.App
}

// New creates a new instance of the FiberLambda object.
// Receives an initialized *fiber.App object - normally created with fiber.New().
// It returns the initialized instance of the FiberLambda object.
func NewALB(app *fiber.App) *FiberLambdaALB {
	return &FiberLambdaALB{
		app: app,
	}
}

// Proxy receives an API Gateway proxy event, transforms it into an http.Request
// object, and sends it to the fiber.App for routing.
// It returns a proxy response object generated from the http.ResponseWriter.
func (f *FiberLambdaALB) Proxy(req events.ALBTargetGroupRequest) (events.ALBTargetGroupResponse, error) {
	fiberRequest, err := f.ProxyEventToHTTPRequest(req)
	return f.proxyInternal(fiberRequest, err)
}

// ProxyWithContext receives context and an API Gateway proxy event,
// transforms them into an http.Request object, and sends it to the echo.Echo for routing.
// It returns a proxy response object generated from the http.ResponseWriter.
func (f *FiberLambdaALB) ProxyWithContext(ctx context.Context, req events.ALBTargetGroupRequest) (events.ALBTargetGroupResponse, error) {
	fiberRequest, err := f.EventToRequestWithContext(ctx, req)
	return f.proxyInternal(fiberRequest, err)
}

func (f *FiberLambdaALB) proxyInternal(req *http.Request, err error) (events.ALBTargetGroupResponse, error) {
	if err != nil {
		return core.GatewayTimeoutALB(), core.NewLoggedError("Could not convert proxy event to request: %v", err)
	}

	respWriter := core.NewProxyResponseWriterALB()
	f.adaptor(http.ResponseWriter(respWriter), req)

	proxyResponse, err := respWriter.GetProxyResponse()
	if err != nil {
		return core.GatewayTimeoutALB(), core.NewLoggedError("Error while generating proxy response: %v", err)
	}

	return proxyResponse, nil
}

func (f *FiberLambdaALB) adaptor(w http.ResponseWriter, r *http.Request) {
	// New fasthttp request
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	// Convert net/http -> fasthttp request
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, utils.StatusMessage(fiber.StatusInternalServerError), fiber.StatusInternalServerError)
		return
	}
	req.Header.SetContentLength(len(body))
	_, _ = req.BodyWriter().Write(body)

	req.Header.SetMethod(r.Method)
	req.SetRequestURI(r.RequestURI)
	req.SetHost(r.Host)
	for key, val := range r.Header {
		for _, v := range val {
			switch key {
			case fiber.HeaderHost,
				fiber.HeaderContentType,
				fiber.HeaderUserAgent,
				fiber.HeaderContentLength,
				fiber.HeaderConnection:
				req.Header.Set(key, v)
			default:
				req.Header.Add(key, v)
			}
		}
	}

	remoteAddr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err != nil {
		http.Error(w, utils.StatusMessage(fiber.StatusInternalServerError), fiber.StatusInternalServerError)
		return
	}

	// New fasthttp Ctx
	var fctx fasthttp.RequestCtx
	fctx.Init(req, remoteAddr, nil)

	// Pass RequestCtx to Fiber router
	f.app.Handler()(&fctx)

	// Set response headers
	fctx.Response.Header.VisitAll(func(k, v []byte) {
		w.Header().Add(utils.UnsafeString(k), utils.UnsafeString(v))
	})

	// Set response statuscode
	w.WriteHeader(fctx.Response.StatusCode())

	// Set response body
	_, _ = w.Write(fctx.Response.Body())
}