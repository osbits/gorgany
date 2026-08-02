# Complex RBAC SQL Examples

> **`field:` is a column reference, and nothing else.**
>
> It was once documented as being able to hold raw SQL, with the caller's attributes
> substituted into it. That was second-order SQL injection: `field: "author_name =
> '{{user_username}}'"` with a username of `x' OR 'a'='a` produced a predicate true for every
> row, and the unparenthesised WHERE clause carried the injected `OR` across its siblings.
>
> A `field:` that is not a bare or dotted column name is now refused when the filters are
> generated. User context reaches SQL through `value:`, which is bound as a query parameter —
> which is what every example below already does. For a predicate that genuinely needs to be
> SQL, use `raw_sql:` with `raw_args:`; placeholders are **not** expanded into `raw_sql`, so
> anything user-derived goes in `raw_args` and is bound:
>
> ```yaml
> - raw_sql: "author_name = ? AND published = true"
>   raw_args: ["{{.Username}}"]
>   logic: "AND"
> ```
>
> Role keys under `custom_filters:` are matched case-insensitively, so `ADMIN:` and `admin:`
> both work. They did not use to: the lookup lower-cased the caller's role and compared it to
> the key verbatim, so the upper-case keys in these examples matched nothing and the filters
> they describe were never applied.

## Scenario: Team Manager Access to Team Members

### Database Schema
```sql
-- People table
CREATE TABLE people (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255),
    email VARCHAR(255),
    phone VARCHAR(255),
    salary DECIMAL(10,2)
);

-- Teams table
CREATE TABLE teams (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255),
    description TEXT
);

-- Team members junction table
CREATE TABLE team_members (
    id SERIAL PRIMARY KEY,
    team_id INTEGER REFERENCES teams(id),
    person_id INTEGER REFERENCES people(id),
    role VARCHAR(50), -- 'manager', 'member'
    UNIQUE(team_id, person_id)
);
```

### Configuration
```yaml
person:
  db_rbac:
    custom_filters:
      TEAM_MANAGER:
        - field: "team_members"
          operator: "subquery"
          subquery:
            table: "team_members"
            select: "person_id"
            operator: "IN"
            where:
              - field: "team_id"
                operator: "="
                value: "{{user_team_id}}"
              - field: "role"
                operator: "="
                value: "member"
```

### Generated SQL for Team Manager
```sql
-- When a team manager (user_id=123, team_id=5) requests people
SELECT * FROM people 
WHERE id IN (
    SELECT person_id 
    FROM team_members 
    WHERE team_id = 5 
      AND role = 'member'
)
LIMIT 20 OFFSET 0;
```

### Generated SQL for Count Query
```sql
-- Count query for pagination
SELECT COUNT(*) FROM people 
WHERE id IN (
    SELECT person_id 
    FROM team_members 
    WHERE team_id = 5 
      AND role = 'member'
);
```

## Scenario: Multi-Level Access (User → Team → Department)

### Configuration
```yaml
project:
  db_rbac:
    custom_filters:
      TEAM_MANAGER:
        - field: "team_projects"
          operator: "subquery"
          subquery:
            table: "project_teams"
            select: "project_id"
            operator: "IN"
            where:
              - field: "team_id"
                operator: "="
                value: "{{user_team_id}}"
      
      DEPARTMENT_MANAGER:
        - field: "department_id"
          operator: "="
          value: "{{user_department_id}}"
```

### Generated SQL for Team Manager
```sql
SELECT * FROM projects 
WHERE id IN (
    SELECT project_id 
    FROM project_teams 
    WHERE team_id = 5
)
LIMIT 20 OFFSET 0;
```

### Generated SQL for Department Manager
```sql
SELECT * FROM projects 
WHERE department_id = 10
LIMIT 20 OFFSET 0;
```

## Scenario: Complex User Access (Direct + Team + Department)

### Configuration
```yaml
document:
  db_rbac:
    custom_filters:
      USER:
        - field: "owner_id"
          operator: "="
          value: "{{user_id}}"
          logic: "OR"
        - field: "team_documents"
          operator: "subquery"
          subquery:
            table: "team_documents"
            select: "document_id"
            operator: "IN"
            where:
              - field: "team_id"
                operator: "="
                value: "{{user_team_id}}"
          logic: "OR"
        - field: "department_id"
          operator: "="
          value: "{{user_department_id}}"
          logic: "OR"
```

### Generated SQL
```sql
SELECT * FROM documents 
WHERE owner_id = 123
   OR id IN (
       SELECT document_id 
       FROM team_documents 
       WHERE team_id = 5
   )
   OR department_id = 10
LIMIT 20 OFFSET 0;
```

## Scenario: Code-Based Custom Processor

### Custom Processor
```go
func TeamManagerFilterProcessor(ctx context.Context, user core.Authenticable, filter DBFilter) ([]DBFilter, error) {
    teamID := user.GetTeamID()
    
    return []DBFilter{
        {
            Field: "team_members",
            Operator: "subquery",
            Subquery: &DBSubquery{
                Table:    "team_members",
                Select:   "person_id",
                Operator: "IN",
                Where: []DBFilter{
                    {
                        Field:    "team_id",
                        Operator: "=",
                        Value:    teamID,
                        Logic:    "AND",
                    },
                },
            },
        },
    }, nil
}
```

### Generated SQL
```sql
SELECT * FROM people 
WHERE id IN (
    SELECT person_id 
    FROM team_members 
    WHERE team_id = 5
)
LIMIT 20 OFFSET 0;
```

## Performance Benefits

### Before (Application-Level Filtering)
```sql
-- Fetch ALL people (1M records)
SELECT * FROM people LIMIT 20 OFFSET 0;

-- Then filter in application:
-- - Load 1M records into memory
-- - Filter by team membership
-- - Return 20 records
-- - Memory usage: ~500MB
-- - Network transfer: ~500MB
```

### After (DB-Level Filtering)
```sql
-- Fetch ONLY team members (50 records)
SELECT * FROM people 
WHERE id IN (
    SELECT person_id 
    FROM team_members 
    WHERE team_id = 5
)
LIMIT 20 OFFSET 0;

-- Memory usage: ~25KB
-- Network transfer: ~25KB
-- Performance improvement: 99.995%
```

## Complex Join Example

### Configuration with Joins
```yaml
person:
  db_rbac:
    custom_filters:
      TEAM_MANAGER:
        - field: "team_members"
          operator: "subquery"
          subquery:
            table: "team_members"
            select: "person_id"
            operator: "IN"
            join:
              - type: "INNER"
                table: "teams"
                left_key: "team_members.team_id"
                right_key: "teams.id"
            where:
              - field: "teams.manager_id"
                operator: "="
                value: "{{user_id}}"
```

### Generated SQL
```sql
SELECT * FROM people 
WHERE id IN (
    SELECT team_members.person_id 
    FROM team_members 
    INNER JOIN teams ON team_members.team_id = teams.id
    WHERE teams.manager_id = 123
)
LIMIT 20 OFFSET 0;
```
