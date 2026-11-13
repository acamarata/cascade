package graph

// SSHTransport must keep satisfying Transport, the interface a later nodelink slice selects by.
var _ Transport = (*SSHTransport)(nil)
