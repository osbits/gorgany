package db

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	DBName   string
}

type PostgresConfig struct {
	Config
	SSLMode              string
	PreferSimpleProtocol bool
}

type Type string

const (
	PostgreSQL Type = "postgres"
	MongoDb         = "mongo"
)
