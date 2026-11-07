package httpapi

import (
	"net/http"

	configurationtransport "bansoko/internal/configuration/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// ListConnections returns the redacted typed connections.
func (s *Server) ListConnections(w http.ResponseWriter, r *http.Request) {
	connections, err := s.deps.Connections.ListConnections(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := configurationtransport.ConnectionsView(connections)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// PutConnection saves one typed connection with If-Match version CAS.
func (s *Server) PutConnection(w http.ResponseWriter, r *http.Request, connectionID generated.ConnectionID, params generated.PutConnectionParams) {
	var input generated.ConnectionInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	mappedInput, err := configurationtransport.ConnectionInput(input, connectionID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	connection, err := s.deps.Connections.PutConnection(r.Context(), connectionID, int64(params.IfMatch), mappedInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := configurationtransport.ConnectionView(connection)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// TestConnection performs a bounded typed probe and reports redacted health.
func (s *Server) TestConnection(w http.ResponseWriter, r *http.Request, connectionID generated.ConnectionID) {
	result, err := s.deps.Connections.TestConnection(r.Context(), connectionID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.ConnectionTestResult(result))
}

// ListNetworkRoutes returns the redacted typed egress routes.
func (s *Server) ListNetworkRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.deps.Connections.ListNetworkRoutes(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := configurationtransport.NetworkRoutesView(routes)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// PutNetworkRoute saves one typed egress route with If-Match version CAS.
func (s *Server) PutNetworkRoute(w http.ResponseWriter, r *http.Request, routeID generated.RouteID, params generated.PutNetworkRouteParams) {
	var input generated.NetworkRouteInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	mappedInput, err := configurationtransport.NetworkRouteInput(input, routeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	route, err := s.deps.Connections.PutNetworkRoute(r.Context(), routeID, int64(params.IfMatch), mappedInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := configurationtransport.NetworkRouteView(route)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// TestNetworkRoute performs a bounded egress probe and reports redacted health.
func (s *Server) TestNetworkRoute(w http.ResponseWriter, r *http.Request, routeID generated.RouteID) {
	result, err := s.deps.Connections.TestNetworkRoute(r.Context(), routeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.NetworkRouteTestResult(result))
}
