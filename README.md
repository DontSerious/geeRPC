# Server

- 使用 `encoding/gob` 实现消息的编解码(序列化与反序列化)

一个典型的 RPC 调用如下：

```
err = client.Call("Arith.Multiply", args, &reply)
```

客户端发送的请求包括服务名 `Arith`，方法名 `Multiply`，参数 `args` 三个，服务端的响应包括错误 `error`，返回值 `reply` 2 个。

## 消息发送

客户端固定采用 JSON 编码 Option，后续的 header 和 body 的编码方式由 Option 中的 CodeType 指定，服务端首先使用 JSON 解码 Option，然后通过 Option 的 CodeType 解码剩余的内容。即报文将以这样的形式发送：

```
| Option{MagicNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |  
| <------      固定 JSON 编码      ------>  | <-------   编码方式由 CodeType 决定   ------->|
```

在一次连接中，Option 固定在报文的最开始，Header 和 Body 可以有多个，即报文可能是这样的。

```
| Option | Header1 | Body1 | Header2 | Body2 | ...
```

## 服务注册

对 `net/rpc` 而言，一个函数需要能够被远程调用，需要满足如下五个条件：

- the method’s type is exported. – 方法所属类型是导出的。
- the method is exported. – 方式是导出的。
- the method has two arguments, both exported (or builtin) types. – 两个入参，均为导出或内置类型。
- the method’s second argument is a pointer. – 第二个入参必须是一个指针。
- the method has return type error. – 返回值为 error 类型。

```
func (t *T) MethodName(argType T1, replyType *T2) error
```

为了解决服务端每暴露一个方法就要编写新的代码，服务端使用**反射**来将这个映射过程自动化。

例如：

```go
func main() {  
	var wg sync.WaitGroup  
	typ := reflect.TypeOf(&wg)  
	for i := 0; i < typ.NumMethod(); i++ {  
		method := typ.Method(i)  
		argv := make([]string, 0, method.Type.NumIn())  
		returns := make([]string, 0, method.Type.NumOut())  
		// j 从 1 开始，第 0 个入参是 wg 自己。  
		for j := 1; j < method.Type.NumIn(); j++ {  
			argv = append(argv, method.Type.In(j).Name())  
		}  
		for j := 0; j < method.Type.NumOut(); j++ {  
			returns = append(returns, method.Type.Out(j).Name())  
		}  
		log.Printf("func (w *%s) %s(%s) %s",  
			typ.Elem().Name(),  
			method.Name,  
			strings.Join(argv, ","),  
			strings.Join(returns, ","))  
    }  
}
```

# Client

对 `net/rpc` 而言，一个函数需要能够被远程调用，需要满足如下五个条件：

- the method’s type is exported.
- the method is exported.
- the method has two arguments, both exported (or builtin) types.
- the method’s second argument is a pointer.
- the method has return type error.

```
func (t *T) MethodName(argType T1, replyType *T2) error
```

# 超时处理

纵观整个远程调用的过程，需要客户端处理超时的地方有：

- 与服务端建立连接，导致的超时
- 发送请求到服务端，写报文导致的超时
- 等待服务端处理时，等待处理导致的超时（比如服务端已挂死，迟迟不响应）
- 从服务端接收响应时，读报文导致的超时

需要服务端处理超时的地方有：

- 读取客户端请求报文时，读报文导致的超时
- 发送响应报文时，写报文导致的超时
- 调用映射服务的方法时，处理报文导致的超时

GeeRPC 在 3 个地方添加了超时处理机制。分别是：

1. 客户端创建连接时  
2. 客户端 `Client.Call()` 整个过程导致的超时（包含发送报文，等待处理，接收报文所有阶段）  
3. 服务端处理报文，即 `Server.handleRequest` 超时。

# HTTP 支持

HTTP 协议的 CONNECT 方法提供了 RPC 和 HTTP 协议转换的能力，CONNECT 一般用于代理服务。

我们先参考通过代理服务器将 HTTP 协议转换为 HTTPS 协议的过程：

1. 浏览器通过 HTTP 明文形式向代理服务器发送一个 CONNECT 请求告诉代理服务器目标地址和端口
2. 代理服务器接收到这个请求后，会在对应端口与目标站点建立一个 TCP 连接，连接建立成功后返回 HTTP 200 状态码告诉浏览器与该站点的加密通道已经完成
3. 接下来代理服务器仅需透传浏览器和服务器之间的加密数据包即可，代理服务器无需解析 HTTPS 报文

例子：

```bash
# 1. 浏览器向代理服务器发送 CONNECT 请求。
CONNECT geektutu.com:443 HTTP/1.0

# 2. 代理服务器返回 HTTP 200 状态码表示连接已经建立。
HTTP/1.0 200 Connection Established

# 3. 之后浏览器和服务器开始 HTTPS 握手并交换加密数据，代理服务器只负责传输彼此的数据包，并不能读取具体数据内容（代理服务器也可以选择安装可信根证书解密 HTTPS 报文）。
```

结论：

- 对 RPC 服务端来，需要做的是将 HTTP 协议转换为 RPC 协议
- 对客户端来说，需要新增通过 HTTP CONNECT 请求创建连接的逻辑

好处：

- RPC 服务仅仅使用了监听端口的 `/_geerpc` 路径，在其他路径上我们可以提供诸如日志、统计等更为丰富的功能。

## 服务端

服务端通信过程应该是这样的：

```bash
# 1. 客户端向 RPC 服务器发送 CONNECT 请求
CONNECT 10.0.0.1:9999/_geerpc_ HTTP/1.0

# 2. RPC 服务器返回 HTTP 200 状态码表示连接建立。
HTTP/1.0 200 Connected to Gee RPC

# 3. 客户端使用创建好的连接发送 RPC 报文，先发送 Option，再发送 N 个请求报文，服务端处理 RPC 请求并响应。
```

`http.Handle` 源码

```go
package http  
// Handle registers the handler for the given pattern  
// in the DefaultServeMux.  
// The documentation for ServeMux explains how patterns are matched.  
func Handle(pattern string, handler Handler) { DefaultServeMux.Handle(pattern, handler) }

type Handler interface {  
    ServeHTTP(w ResponseWriter, r *Request)  
}
```

只需要实现接口 Handler 即可作为一个 HTTP Handler 处理 HTTP 请求。接口 Handler 只定义了一个方法 `ServeHTTP`，实现该方法即可。
# 负载均衡

- 随机选择策略 - 从服务列表中随机选择一个。
- 轮询算法(Round Robin) - 依次调度不同的服务器，每次调度执行 i = (i + 1) mode n。
- 加权轮询(Weight Round Robin) - 在轮询算法的基础上，为每个服务实例设置一个权重，高性能的机器赋予更高的权重，也可以根据服务实例的当前的负载情况做动态的调整，例如考虑最近5分钟部署服务器的 CPU、内存消耗情况。
- 哈希/一致性哈希策略 - 依据请求的某些特征，计算一个 hash 值，根据 hash 值将请求发送到对应的机器。一致性 hash 还可以解决服务实例动态添加情况下，调度抖动的问题。一致性哈希的一个典型应用场景是分布式缓存服务。

简单起见，GeeRPC 仅实现 Random 和 RoundRobin 两种策略。


# 注册中心

客户端和服务端都只需要感知注册中心的存在，而不需要感知对方的存在。

1. 服务端启动后，向注册中心发送注册消息，注册中心得知该服务已经启动，处于可用状态。一般来说，服务端还需要定期向注册中心发送心跳，证明自己还活着。
2. 客户端向注册中心询问，当前哪天服务是可用的，注册中心将可用的服务列表返回客户端。
3. 客户端根据注册中心得到的服务列表，选择其中一个发起调用。

比较常用的注册中心有 [etcd](https://github.com/etcd-io/etcd)、[zookeeper](https://github.com/apache/zookeeper)、[consul](https://github.com/hashicorp/consul)，一般比较出名的微服务或者 RPC 框架，这些主流的注册中心都是支持的。
