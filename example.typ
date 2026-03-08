
= D2 Diagrams in Typst

== Simple Example

#d2[
  x -> y -> z
]

== With Options

#d2(layout: "elk", theme: "0", sketch: "true")[
  direction: right
  
  user: User {
    shape: person
  }
  
  app: Application {
    ui: Web Interface
    api: REST API
    db: Database {
      shape: cylinder
    }
  }
  
  user -> app.ui: Browse
  app.ui -> app.api: Request
  app.api -> app.db: Query
]

== Complex Architecture

#d2(layout: "elk", theme: "0")[
  direction: right
  
  client: Client {
    browser: Browser
    mobile: Mobile App
  }
  
  lb: Load Balancer
  
  backend: Backend {
    api: API Gateway
    auth: Auth Service
    workers: Job Queue {
      shape: queue
    }
  }
  
  data: Data Layer {
    postgres: PostgreSQL {
      shape: cylinder
    }
    redis: Redis Cache {
      shape: cylinder
    }
  }
  
  client.browser -> lb
  client.mobile -> lb
  lb -> backend.api
  lb -> backend.auth
  backend.api -> backend.workers
  backend.api -> data.postgres
  backend.auth -> data.redis
  backend.workers -> data.postgres
]
