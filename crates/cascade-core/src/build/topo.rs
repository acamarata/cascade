//! Topological ordering for build tickets.

use std::collections::{HashMap, HashSet};

use cascade_types::error::{CascadeError, Result};

use crate::pbd::schema::Ticket;

/// Kahn's algorithm over ticket `depends_on` edges.
///
/// Returns ticket IDs in an order where every dependency appears before its
/// dependent. Returns `Err` if a cycle is detected.
pub(super) fn topological_sort(tickets: &[Ticket]) -> Result<Vec<String>> {
    let mut in_degree: HashMap<&str, usize> = HashMap::new();
    let mut adj: HashMap<&str, Vec<&str>> = HashMap::new();

    for ticket in tickets {
        in_degree.entry(ticket.id.as_str()).or_insert(0);
        adj.entry(ticket.id.as_str()).or_default();
    }
    for ticket in tickets {
        for dependency in &ticket.depends_on {
            // dependency must finish before ticket — dependency → ticket edge
            adj.entry(dependency.as_str())
                .or_default()
                .push(ticket.id.as_str());
            *in_degree.entry(ticket.id.as_str()).or_insert(0) += 1;
        }
    }

    let mut queue: Vec<&str> = in_degree
        .iter()
        .filter(|(_, &degree)| degree == 0)
        .map(|(&id, _)| id)
        .collect();
    queue.sort(); // deterministic ordering

    let mut result: Vec<String> = Vec::new();
    let mut visited: HashSet<&str> = HashSet::new();

    while !queue.is_empty() {
        queue.sort(); // keep deterministic across iterations
        let node = queue.remove(0);
        if visited.contains(node) {
            continue;
        }
        visited.insert(node);
        result.push(node.to_string());
        if let Some(neighbors) = adj.get(node) {
            for &neighbor in neighbors {
                let degree = in_degree.entry(neighbor).or_insert(0);
                *degree = degree.saturating_sub(1);
                if *degree == 0 {
                    queue.push(neighbor);
                }
            }
        }
    }

    if result.len() != tickets.len() {
        return Err(CascadeError::Other(
            "ticket dependency cycle detected".to_string(),
        ));
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    use crate::pbd::schema::TicketStatus;

    fn make_ticket(id: &str, dependencies: Vec<&str>) -> Ticket {
        Ticket {
            id: id.into(),
            sprint_id: "s01".into(),
            title: id.into(),
            status: TicketStatus::Queue,
            steps: vec![],
            depends_on: dependencies.into_iter().map(String::from).collect(),
            repo: None,
            weight: None,
            note: None,
            blocked_reason: None,
        }
    }

    #[tokio::test]
    async fn topological_sort_respects_deps() {
        let tickets = vec![
            make_ticket("t03", vec!["t01"]),
            make_ticket("t01", vec![]),
            make_ticket("t02", vec![]),
        ];

        let order = topological_sort(&tickets).expect("no cycle");
        let positions: HashMap<_, _> = order
            .iter()
            .enumerate()
            .map(|(index, id)| (id.as_str(), index))
            .collect();
        assert!(positions["t01"] < positions["t03"], "t01 must precede t03");
    }

    #[test]
    fn topological_sort_detects_cycle() {
        let tickets = vec![
            make_ticket("ta", vec!["tb"]),
            make_ticket("tb", vec!["ta"]),
        ];
        assert!(
            topological_sort(&tickets).is_err(),
            "cycle should be detected"
        );
    }
}
