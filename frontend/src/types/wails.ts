// This file is auto-generated. DO NOT EDIT.

export interface Connection {
  id: string;
  name: string;
  type: string;
  host: string;
  port: number;
  username: string;
  password: string;
  database: string;
  sslMode: string;
  options: Record<string, string>;
}

export interface ConnectionConfig {
  name: string;
  type: string;
  host: string;
  port: number;
  username: string;
  password: string;
  database: string;
  sslMode: string;
  options: Record<string, string>;
}

export interface QueryResult {
  columns: Column[];
  rows: any[][];
  rowCount: number;
  affectedRows: number;
  lastInsertId: number;
  executionTime: number;
  error: string;
}

export interface Column {
  name: string;
  type: string;
  nullable: boolean;
}

export interface TableInfo {
  name: string;
  schema: string;
  type: string;
  comment: string;
}

export interface ColumnInfo {
  name: string;
  type: string;
  nullable: boolean;
  defaultValue: string;
  isPrimaryKey: boolean;
  comment: string;
}

declare global {
  interface Window {
    go: {
      main: {
        App: {
          TestConnection(config: ConnectionConfig): Promise<void>;
          GetConnections(): Promise<Connection[]>;
          AddConnection(config: ConnectionConfig): Promise<string>;
          UpdateConnection(id: string, config: ConnectionConfig): Promise<void>;
          DeleteConnection(id: string): Promise<void>;
          Connect(id: string): Promise<void>;
          Disconnect(id: string): Promise<void>;
          ExecuteQuery(connectionID: string, query: string): Promise<QueryResult>;
          GetDatabases(connectionID: string): Promise<string[]>;
          GetTables(connectionID: string, database: string): Promise<TableInfo[]>;
          GetColumns(connectionID: string, database: string, table: string): Promise<ColumnInfo[]>;
        };
      };
    };
    runtime: {
      EventsOn(eventName: string, callback: (...data: any[]) => void): void;
      EventsOff(eventName: string): void;
    };
  }
}

export {};
