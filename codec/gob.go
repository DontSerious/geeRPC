package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadWriteCloser // TCP or socket instance
	buf  *bufio.Writer      // buff improve performance
	dec  *gob.Decoder
	enc  *gob.Encoder
}

// implement interface
func (c *GobCodec) Close() error {
	return c.conn.Close()
}

func (c *GobCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}

func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		}
	}()

	if err := c.enc.Encode(h); err != nil {
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}

	if err := c.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob error encoding body:", err)
		return err
	}

	return nil
}

// 接口声明，编译器会检查GobCodec类型是否实现了Codec接口。如果没有实现，编译器会报错
// = (*GobCodec)(nil) 是一个类型断言，表面是将nil转换为*GobCodec类型
// 但其实它目的不是为了实际地执行转换，而是为了检查*GobCodec类型是否实现了Codec接口
var _ Codec = (*GobCodec)(nil)

// factory 这部分代码和工厂模式类似，与工厂模式不同的是，返回的是构造函数，而非实例。
func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}
