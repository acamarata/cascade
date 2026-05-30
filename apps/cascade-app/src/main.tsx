/**
 * Purpose: React entry point — mounts the root App component.
 * Inputs:  DOM element #root (created in index.html)
 * Outputs: Rendered React tree
 * Constraints: StrictMode enabled to surface double-render bugs early.
 * SPORT: MASTER-COMPONENTS.md — App root
 */

import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./styles/globals.css";

const root = document.getElementById("root");
if (!root) throw new Error("Root element #root not found in DOM");

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
