package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3def5c

// 协商编码方式
// | Option{MagicNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |
// | <------      固定 JSON 编码      ------>  | <-------   编码方式由 CodeType 决定   ------->|
// 实际报文样式
// | Option | Header1 | Body1 | Header2 | Body2 | ...

type Option struct {
	MagicNumber    int           // MagicNumber marks this's a geerpc request
	CodecType      codec.Type    // client may choose diff Codec to encode body
	ConnectTimeout time.Duration // 0 means no limit
	HandleTimeout  time.Duration
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

// Server represents an RPC Server
type Server struct {
	serviceMap sync.Map
}

func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

func Register(rcvr interface{}) error {
	return DefaultServer.Register(rcvr)
}

func (server *Server) findService(serviceMethod string) (svc *service, mType *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc service: service/method request ill-formed: " + serviceMethod)
		return
	}

	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svcI, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service: " + serviceName)
		return
	}

	svc = svcI.(*service)
	mType = svc.method[methodName]
	if mType == nil {
		err = errors.New("rpc server: can't find method: " + methodName)
	}
	return
}

// NewServer returns a new Server
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of *Server
var DefaultServer = NewServer()

// accept all income connection and create an goroutine to handle
func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		go server.ServerConn(conn)
	}
}

func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// ServerConn runs the server on a single connection.
// ServerConn blocks, serving the connection until the client hangs up.
func (server *Server) ServerConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()

	var opt Option

	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error:", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Println("rpc server: invalid magic number:", opt.MagicNumber)
		return
	}
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Println("rpc server: invalid codec type:", opt.CodecType)
		return
	}

	server.ServerCodec(f(conn), opt.ConnectTimeout)
}

// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

// handle connection
func (server *Server) ServerCodec(cc codec.Codec, timeout time.Duration) {
	sending := new(sync.Mutex) // make sure to send a complete response, promise data race won't happen
	wg := new(sync.WaitGroup)  // wait until all request are handled
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break // already cannot recover, close connection
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, timeout)
	}
	wg.Wait()
	_ = cc.Close()
}

type request struct {
	h            *codec.Header
	argv, replyv reflect.Value // argv and replyv of request
	mType        *methodType
	svc          *service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}

	req := &request{h: h}
	req.svc, req.mType, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, nil
	}
	req.argv = req.mType.newArgv()
	req.replyv = req.mType.newReplyv()

	// make sure the argvI is a pointer, ReadBody need a pointer as a parameter
	argvI := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvI = req.argv.Addr().Interface()
	}
	if err = cc.ReadBody(argvI); err != nil {
		log.Println("rpc server: read argv error:", err)
		return req, err
	}

	return req, nil
}

/*
make sure sendRequest() only invoke once.

The whole process is split into two phases: called and sent. Two condition occur:
1. called channel receive message, represents processing without timeout.
2. time.After() receive message before called channel, represents timeout, called and sent channel will block, invoke case <-time.After(timeout)'s code.
*/
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mType, req.argv, req.replyv)
		// Sending an empty struct can be thought of as sending a signal, because sizeof that is zero, save memory
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{}
			return
		}
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()

	if timeout == 0 {
		// wait the empty struct signal and throw it, then execute the next statement.
		<-called
		<-sent
		return
	}

	select {
	// use time.After() to set a timer, when the set time is over, select will go into this case.
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	// if called channel get message early than time.After(), function will wait sent channel's message to return.
	case <-called:
		<-sent
	}
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}
