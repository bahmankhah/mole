package modules

// PortRisk explains why an open port is often a security problem, and what to do about it.
// An empty string means the port is not flagged.
func PortRisk(port int) string {
	return portRisks[port]
}

func same(msg string, ports ...int) {
	for _, port := range ports {
		portRisks[port] = msg
	}
}

var portRisks = map[int]string{}

func init() {
	same("SSH is a common guess-the-password target. Use key login, disable password login, and limit who can reach it (VPN or an allow-list) instead of leaving it open to the internet.",
		22, 2222, 2022)
	same("Remote Desktop is scanned constantly. Do not leave it on the public internet. Reach it through a VPN and require strong, unique accounts.",
		3389)
	same("Telnet sends the username and password in cleartext. Turn it off and use SSH.",
		23)
	same("VNC is often protected by a weak password, or none. Do not expose it. Reach the screen through SSH or a VPN.",
		5900, 5901, 5902, 5800)
	same("WinRM can run commands on the computer. Keep it on the management network only, and require authentication.",
		5985, 5986)
	same("These legacy remote-login services have little or no real protection. Disable them and use SSH.",
		512, 513)
	same("Router admin interfaces are a frequent break-in path. Allow them only from a management network, and change the default password.",
		8291, 8728, 8729)
	same("This remote-admin tool should not be reachable from the internet. Put it behind a VPN.",
		4899)
	same("X11 can expose the screen and what is typed. Bind it to localhost.",
		6000, 6001)

	same("FTP sends passwords in cleartext, and anonymous login is sometimes left on. Use SFTP instead, and close this port to the internet.",
		21, 20, 2121)
	same("File transfer should be limited to the addresses that need it, not the whole internet.",
		990, 989)
	same("Windows file sharing is a common ransomware entry point. Keep it off every interface that faces the internet.",
		445, 139)
	same("Windows RPC is an internal service. It should not be reachable from outside the local network.",
		135, 593)
	same("Network file shares are often readable or writable with little authentication. Bind them to a private network.",
		2049, 111, 4045, 548)
	same("rsync is sometimes anonymous and writable. Require authentication, and do not publish it on the internet.",
		873)

	same("Redis often has no password, so anyone who can connect can read or change data. Bind it to localhost and set a password.",
		6379, 6380, 26379)
	same("MongoDB is frequently left with authentication off. Turn on access control and do not expose this port.",
		27017, 27018, 27019)
	same("Elasticsearch often lets anyone read or delete data. Enable its security features and keep both the HTTP and transport ports private.",
		9200, 9300)
	same("Memcached has no authentication. Bind it to localhost so strangers cannot read the cache or bounce traffic off it.",
		11211)
	same("This is a database port. It should listen on localhost or a private network only, with authentication required. Do not leave it open to the internet.",
		3306, 3307, 33060, 5432, 5433, 6432, 1433, 1521, 2483, 2484, 9042, 9160, 7199, 7000,
		3050, 50000, 26257, 28015, 8091, 11210, 8428, 6333, 6334, 19530, 7700, 8108, 9009)
	same("This data or admin service is often installed without a login. Bind it to localhost or a private network, and turn authentication on.",
		7474, 7687, 8529, 5984, 8086, 8123, 9000, 28017)

	same("The Docker API is control of the host. The plain port has no authentication at all. Never expose it. Use a local socket instead.",
		2375, 4243)
	same("The Docker API can start containers and should only be reachable by administrators, with TLS and authentication.",
		2376)
	same("This is the Kubernetes control plane. Do not leave it open to the world. Use private access and strong authentication.",
		6443)
	same("Kubelet can reveal or control workloads, and the read-only port is often unauthenticated. Close both to the public internet.",
		10250, 10255)
	same("etcd stores cluster secrets, including credentials. Only cluster members should be able to reach it.",
		2379, 2380)
	same("Consul's HTTP and RPC ports can change cluster data. Keep them on the private network and require access control.",
		8500, 8300)
	same("Vault stores secrets. Allow it only from the networks that need it, over TLS.",
		8200, 8201)
	same("Kafka and ZooKeeper are internal cluster ports. They should not be reachable from the internet.",
		9092, 9093, 2181, 2888, 3888)
	same("The Git daemon can publish repositories with no login. Close it unless those repositories are meant to be public.",
		9418)
	same("Message brokers and their admin pages often still use the default login. Do not expose them. Reach the admin page through a VPN, and change the password.",
		5672, 5671, 15672, 15671, 25672, 4369, 61616, 61613, 8161, 4222, 6222)
	same("Tomcat's AJP port is a private link between the web server and Tomcat. It must not be reachable from outside.",
		8009)
	same("Java RMI can reach application internals. Keep the registry on localhost.",
		1099)
	same("A container registry may allow anyone to pull or push images. Require a login, and do not leave it public.",
		5000)
	same("This cluster scheduler API should stay on the private network.",
		4646, 4647, 7077)
	same("Hadoop and YARN admin ports show cluster data and are often unauthenticated. Keep them private.",
		8020, 9870, 9864, 8042, 8088)
	same("This UI often has no login and shows internal jobs or data. Do not publish it on the internet.",
		4040, 18080, 5601, 9090, 9091, 19999, 3100, 8222, 9600)
	same("Metrics and monitoring agents leak host details and sometimes allow checks to be run. Scrape them only from the private network.",
		9100, 10050, 10051, 5666, 6556, 5044)
	same("A vulnerability-scanner or security-tool console is sensitive. Restrict who can reach it.",
		8834, 55000)
	same("A local AI or notebook server can run work on this machine for anyone who can connect. Bind it to localhost and require a token or login.",
		11434, 8888, 8787, 7860, 8188, 6006)
	same("Admin consoles on this port (Jenkins, Tomcat, and similar) often still have a weak or default login. Confirm authentication is on, and do not expose an admin UI directly.",
		8080, 8081)
	same("This is an admin console. Do not leave it on the public internet. Put it behind a VPN or an allow-list, and change the default password.",
		8443, 9443, 10000, 2082, 2083, 2086, 2087, 2095, 2096, 4848, 7001, 7002, 9080, 9990, 8880, 9001, 20000)
	same("Grafana and similar tools on this port often still use admin/admin. Change that password, and do not expose an operator UI if it does not need to be public.",
		3000)

	same("An open SMTP server can relay spam if it accepts mail for other domains. Allow relay only for authenticated users.",
		25)
	same("Plain POP and IMAP send the mailbox password in cleartext. Disable them and use the TLS ports (993 and 995).",
		110, 143)
	same("This port is also used by the old rsh remote shell, which has no real protection, and by syslog, which can be filled with fake events. Do not expose it.",
		514, 601)
	same("If this DNS server answers recursive queries for everyone, strangers can use it. Allow recursion only for your own networks.",
		53)

	same("Industrial protocols are often unauthenticated and can change physical equipment. They must not be reachable from the internet.",
		502, 102, 44818, 4840, 1911)
	same("An open proxy lets other people send traffic through this host. Turn it off, or require a login and an allow-list.",
		1080, 3128, 8118, 9050)
	same("Cameras and recorders often still use the default password. Do not expose them. View them through a VPN.",
		37777, 554, 8554)
	same("PPTP is an obsolete VPN. Replace it with a current VPN and close this port.",
		1723)
	same("MQTT without authentication lets anyone publish and subscribe. Require a login and TLS, and do not leave it open.",
		1883)
}
