package api

import "net/http"

type route struct {
	pattern string
	handler http.HandlerFunc
}

// routes is everything the agent answers: what a Boxhand needs for one remote environment. Accounts, tokens,
// settings, schedules and notifications stay with that Boxhand.
func (s *Server) routes() []route {
	return []route{
		{"GET /api/info", s.info},
		{"GET /api/dashboard", s.dashboard},
		{"GET /api/envs", s.listEnvs},
		{"POST /api/envs/{id}/start", s.startDaemon},
		{"POST /api/envs/{id}/stop", s.stopDaemon},
		{"GET /api/envs/{id}/doctor", s.doctor},

		{"GET /api/boxes", s.listBoxes},
		{"GET /api/boxes/{name}", s.inspectBox},
		{"POST /api/boxes/{name}/{action}", s.boxAction},
		{"DELETE /api/boxes/{name}", s.removeBox},
		{"GET /api/boxes/{name}/logs", s.logs},
		{"GET /api/logs", s.multiLogs},
		{"GET /api/stats", s.stats},
		{"GET /api/events", s.events},
		{"POST /api/run", s.runScript},
		{"GET /api/sdks", s.listSDKs},
		{"GET /api/sdks/{name}", s.sdkInfo},
		{"GET /api/storage", s.listStorage},
		{"GET /api/storage/{storage}/{script}", s.readStorage},

		{"GET /api/projects", s.listProjects},
		{"POST /api/projects", s.addProject},
		{"GET /api/projects/{id}", s.getProject},
		{"PUT /api/projects/{id}", s.saveProject},
		{"DELETE /api/projects/{id}", s.removeProject},
		{"POST /api/projects/{id}/{action}", s.projectAction},
		{"GET /api/projects/{id}/files", s.projectFiles},
		{"GET /api/projects/{id}/file", s.readProjectFile},
		{"PUT /api/projects/{id}/file", s.writeProjectFile},
		{"POST /api/projects/{id}/check", s.checkProject},
		{"GET /api/projects/{id}/history", s.projectHistory},
		{"GET /api/projects/{id}/history/{version}", s.projectVersion},
		{"POST /api/projects/{id}/history/{version}/restore", s.restoreVersion},
	}
}
