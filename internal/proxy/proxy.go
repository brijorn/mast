package proxy

import (
	"io"
	"maps"
	"net"
	"net/http"
)

type Server struct {
	Addr   string
	Client http.Client
}

func NewServer(addr string) *Server {
	return &Server{
		Addr:   addr,
		Client: http.Client{},
	}
}

func (s *Server) Handler() http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.Method == http.MethodConnect {

			targetConn, err := net.Dial("tcp", r.Host)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = targetConn.Close() }()

			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijacking not supported", http.StatusInternalServerError)
				return
			}
			clientConn, _, err := hijacker.Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = clientConn.Close() }()

			if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
				return
			}

			go func() {
				_, _ = io.Copy(targetConn, clientConn)
			}()
			_, _ = io.Copy(clientConn, targetConn)
		} else {
			req, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			req.Header = r.Header

			res, err := s.Client.Do(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = res.Body.Close() }()

			maps.Copy(w.Header(), res.Header)
			w.WriteHeader(res.StatusCode)
			if _, err := io.Copy(w, res.Body); err != nil {
				return
			}

		}

	})
}
func (s *Server) Listen() error {

	return http.ListenAndServe(s.Addr, s.Handler())
}
