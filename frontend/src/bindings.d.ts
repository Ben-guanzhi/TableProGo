declare module '@bindings/tablepro-go/app' {
  export function AddConnection(config: any): Promise<void>;
  export function Connect(id: string): Promise<void>;
  export function DeleteConnection(index: number): Promise<void>;
  export function Disconnect(connID: string): Promise<void>;
  export function ExecuteQuery(connID: string, query: string): Promise<any>;
  export function GetColumns(connID: string, database: string, table: string): Promise<any[]>;
  export function GetConnections(): Promise<any[]>;
  export function GetCreateTableTemplate(dbType: string, schema: string): Promise<string>;
  export function GetDatabases(connID: string): Promise<string[]>;
  export function GetTables(connID: string, database: string): Promise<any[]>;
  export function Init(): Promise<void>;
  export function Shutdown(): Promise<void>;
  export function TestConnection(config: any): Promise<void>;
}

declare module '@bindings/tablepro-go/models' {
  export class Column {
    name: string;
    type: string;
    nullable: boolean;
    static createFrom(source: any): Column;
  }

  export class ColumnInfo {
    name: string;
    type: string;
    nullable: boolean;
    defaultValue?: string;
    isPrimaryKey: boolean;
    comment: string;
    static createFrom(source: any): ColumnInfo;
  }

  export class ConnectionConfig {
    name: string;
    type: string;
    host: string;
    port: number;
    username: string;
    password: string;
    database: string;
    sslMode: string;
    options: Record<string, string>;
    static createFrom(source: any): ConnectionConfig;
  }

  export class QueryResult {
    columns: Column[];
    rows: any[][];
    rowCount: number;
    affectedRows: number;
    lastInsertId: number;
    executionTime: number;
    error?: string;
    static createFrom(source: any): QueryResult;
  }

  export class TableInfo {
    name: string;
    schema: string;
    type: string;
    comment: string;
    static createFrom(source: any): TableInfo;
  }
}
