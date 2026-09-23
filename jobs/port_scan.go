package jobs

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

// ErrPortScanRunning is returned when a port scan is already in progress.
var ErrPortScanRunning = errors.New("a port scan is already running")

// CreatePortScan resolves the target and scans well-known TCP ports.
// Open ports are saved as they are found.
func (m *Manager) CreatePortScan(target string) (*models.PortScanJob, error) {
	host, err := modules.NormalizeScanTarget(target)
	if err != nil {
		return nil, err
	}

	m.portScanMu.Lock()
	busy := len(m.portScans) > 0 || m.closed
	m.portScanMu.Unlock()
	if busy {
		if m.closed {
			return nil, errors.New("server is shutting down")
		}
		return nil, ErrPortScanRunning
	}

	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 8*time.Second)
	ip, err := modules.ResolveScanTarget(resolveCtx, host)
	resolveCancel()
	if err != nil {
		return nil, err
	}

	ports := modules.KnownPorts()
	now := time.Now()
	job := &models.PortScanJob{
		Target:     host,
		ResolvedIP: ip,
		Status:     models.JobStatusRunning,
		PortsTotal: len(ports),
		StartedAt:  &now,
	}
	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	scanCtx, scanCancel := context.WithCancel(context.Background())
	m.portScanMu.Lock()
	if m.closed || len(m.portScans) > 0 {
		m.portScanMu.Unlock()
		scanCancel()
		_ = m.db.Delete(job).Error
		if m.closed {
			return nil, errors.New("server is shutting down")
		}
		return nil, ErrPortScanRunning
	}
	if m.portScans == nil {
		m.portScans = map[string]context.CancelFunc{}
	}
	m.portScans[job.ID] = scanCancel
	m.portScanMu.Unlock()

	go m.runPortScan(scanCtx, job.ID, ip, host, ports)
	log.Printf("[JobManager] Port scan %s started for %s (%s), %d ports", job.ID, host, ip, len(ports))
	return job, nil
}

func (m *Manager) runPortScan(ctx context.Context, jobID, dialHost, nameHost string, ports []modules.PortService) {
	defer func() {
		m.portScanMu.Lock()
		delete(m.portScans, jobID)
		m.portScanMu.Unlock()
	}()

	open := 0
	last := 0
	err := m.portScanner.ScanPorts(ctx, dialHost, nameHost, ports, func(scanned int, hit *modules.PortHit) {
		last = scanned
		if hit != nil {
			rec := &models.DiscoveredPort{
				PortScanJobID: jobID,
				Port:          hit.Port,
				Protocol:      hit.Protocol,
				Service:       hit.Service,
				Detail:        hit.Detail,
				Group:         hit.Group,
				Banner:        hit.Banner,
				Product:       hit.Product,
				Warning:       hit.Risk,
				Importance:    hit.Importance,
				LatencyMS:     hit.LatencyMS,
			}
			if createErr := m.db.Create(rec).Error; createErr != nil {
				log.Printf("[JobManager] Failed to save port %d for %s: %v", hit.Port, jobID, createErr)
			} else {
				open++
				log.Printf("[JobManager] Open port %d/%s %s on %s", hit.Port, hit.Protocol, hit.Service, nameHost)
			}
		}
		if hit != nil || scanned%5 == 0 {
			m.db.Model(&models.PortScanJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
				"ports_scanned": scanned,
				"ports_open":    open,
			})
		}
	})

	status := models.JobStatusCompleted
	msg := ""
	switch {
	case errors.Is(err, context.Canceled):
		status = models.JobStatusCancelled
		msg = "Scan stopped"
	case err != nil:
		status = models.JobStatusFailed
		msg = err.Error()
	}
	now := time.Now()
	m.db.Model(&models.PortScanJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"status":        status,
		"completed_at":  &now,
		"ports_scanned": last,
		"ports_open":    open,
		"error_message": msg,
	})
	log.Printf("[JobManager] Port scan %s finished: %s, %d open, %d probed", jobID, status, open, last)
}

// StopPortScan cancels a running scan.
func (m *Manager) StopPortScan(jobID string) error {
	m.portScanMu.Lock()
	cancel, ok := m.portScans[jobID]
	m.portScanMu.Unlock()
	if !ok {
		return errors.New("port scan is not running")
	}
	cancel()
	return nil
}

// ActivePortScanID returns the id of the scan in progress, if any.
func (m *Manager) ActivePortScanID() string {
	m.portScanMu.Lock()
	defer m.portScanMu.Unlock()
	for id := range m.portScans {
		return id
	}
	return ""
}

// StopAllPortScans cancels every running scan.
func (m *Manager) StopAllPortScans() {
	m.portScanMu.Lock()
	cancels := m.portScans
	m.portScans = map[string]context.CancelFunc{}
	m.portScanMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (m *Manager) abandonStalePortScans() {
	m.db.Model(&models.PortScanJob{}).
		Where("status IN ?", []string{string(models.JobStatusRunning), string(models.JobStatusPaused)}).
		Updates(map[string]interface{}{
			"status":        models.JobStatusCancelled,
			"error_message": "Scan interrupted by server restart",
		})
}

// GetPortScans returns port scan jobs, newest first.
func (m *Manager) GetPortScans(limit, offset int) ([]models.PortScanJob, int64, error) {
	var jobs []models.PortScanJob
	var total int64
	m.db.Model(&models.PortScanJob{}).Count(&total)
	if err := m.db.Order("created_at DESC").Limit(limit).Offset(offset).Find(&jobs).Error; err != nil {
		return nil, 0, err
	}
	return jobs, total, nil
}

// DeletePortScan stops a running scan if needed and removes the job and its ports.
func (m *Manager) DeletePortScan(jobID string) error {
	if _, err := m.GetPortScan(jobID); err != nil {
		return err
	}
	_ = m.StopPortScan(jobID)
	m.db.Where("port_scan_job_id = ?", jobID).Delete(&models.DiscoveredPort{})
	return m.db.Delete(&models.PortScanJob{}, "id = ?", jobID).Error
}

// GetPortScan returns one port scan job.
func (m *Manager) GetPortScan(jobID string) (*models.PortScanJob, error) {
	var job models.PortScanJob
	if err := m.db.First(&job, "id = ?", jobID).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// GetDiscoveredPorts returns open ports for a scan, most important first.
func (m *Manager) GetDiscoveredPorts(jobID string) ([]models.DiscoveredPort, error) {
	var ports []models.DiscoveredPort
	err := m.db.Where("port_scan_job_id = ?", jobID).
		Order("importance ASC, port ASC").
		Find(&ports).Error
	if err != nil {
		return nil, err
	}
	for i := range ports {
		if ports[i].Warning == "" {
			ports[i].Warning = modules.PortRisk(ports[i].Port)
		}
	}
	return ports, nil
}
