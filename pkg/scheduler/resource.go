package scheduler

/** Filter **/
// filter node according to the node and its gpu model
func (kss *KubeShareScheduler) filterNode(nodeName, model string, request float64, memory int64) (bool, float64, int64) {
	kss.ksl.Debugf("filterNode: node %v with gpu model %v", nodeName, model)

	ok := false
	available := 0.0
	freeMemory := int64(0)
	freeList := kss.cellFreeList[model]
	for _, cellList := range freeList {
		for _, cell := range cellList {
			fit, currentAvailable, currentMemory := kss.checkCellResource(cell, nodeName, request, memory)
			ok = ok || fit
			available += currentAvailable
			freeMemory += currentMemory

			if ok {
				return ok, available, freeMemory
			}
		}
	}

	return ok, available, freeMemory
}

// check if the gpu resource in the node can fit the pod requirement
// and calculate its free resource in the specified gpu model
func (kss *KubeShareScheduler) checkCellResource(cell *Cell, nodeName string, request float64, memory int64) (bool, float64, int64) {
	s := NewStack()

	node := cell.node
	// this cell does not need to check
	if node != nodeName && node != "" {
		return false, 0.0, 0
	}

	if cell.healthy {
		s.Push(cell)
	}

	multiGPU := request > 1.0

	for s.Len() > 0 {
		current := s.Pop()
		kss.ksl.Debugf("Check resource cell: %+v", current)

		if current.node == nodeName && current.healthy {
			// only need whole gpu
			if multiGPU {
				availableWholeCell := current.availableWholeCell
				freeMemory := current.freeMemory
				if availableWholeCell >= request && freeMemory >= memory {
					return true, availableWholeCell, freeMemory
				} else {
					return false, availableWholeCell, freeMemory
				}
			} else {
				if current.level == 1 && current.available >= request && current.freeMemory >= memory {
					return true, current.available, current.freeMemory
				}
			}
		}

		child := current.child
		if child == nil {
			continue
		}

		for i := range child {
			node := child[i].node
			if (node == nodeName || node == "") && child[i].healthy {
				kss.ksl.Debugf("Check resource child: %+v", child[i])
				s.Push(child[i])
			}
		}
	}
	return false, 0, 0
}

/*
func (kss *KubeShareScheduler) checkCellResource(cell *Cell, nodeName string, request float64, memory int64) (bool, float64, int64) {
	s := NewStack()

	node := cell.node
	// this cell does not need to check
	if node != nodeName && node != "" {
		return false, 0.0, 0
	}

	if node == "" || cell.healthy {
		s.Push(cell)
	}

	// store the number of whole gpu
	available := 0.0
	freeMemory := int64(0)
	multiGPU := request > 1.0

	for s.Len() > 0 {
		current := s.Pop()
		kss.ksl.Debugf("Check resource cell: %+v", current)

		if current.level == 1 {

			if multiGPU {
				if current.available == 1.0 {
					available += 1.0
					freeMemory += current.freeMemory

					if available >= request && freeMemory >= memory {
						return true, available, freeMemory
					}
				}
			} else {
				if current.available >= request && current.freeMemory >= memory {
					return true, current.available, current.freeMemory
				}
			}

		}

		child := current.child
		if child == nil {
			continue
		}

		for i := range child {
			node := child[i].node
			if (node == nodeName || node == "") && child[i].healthy {
				kss.ksl.Debugf("Check resource child: %+v", child[i])
				s.Push(child[i])
			}
		}
	}
	return false, available, freeMemory
}
*/
/** Score **/

// for regular pod
// kubeshare treats gpu resource as rare resource
// if the node without gpu, node score will be set to 100
// otherwise, node score will be set to be 0
func (kss *KubeShareScheduler) calculateRegularPodNodeScore(nodeName string) int64 {

	if len(kss.gpuInfos[nodeName]) > 0 {
		return int64(100)
	}

	return int64(0)
}

// for opportunistic pod

func (kss *KubeShareScheduler) calculateOpportunisticPodScore(nodeName string, podStatus *PodStatus) int64 {

	model := podStatus.model
	score := int64(0)
	// assigned gpu model
	if model != "" {

		score = kss.calculateOpportunisticPodNodeScore(kss.getModelLeafCellbyNode(nodeName, model))

	} else {
		//get the gpu information in the node
		score = kss.calculateOpportunisticPodNodeScore(kss.getAllLeafCellbyNode(nodeName))
	}
	return score
}

// get the leaf cell according to node's model
func (kss *KubeShareScheduler) getModelLeafCellbyNode(nodeName, model string) CellList {

	var cl CellList
	freeList := kss.cellFreeList[model]
	for _, cellList := range freeList {
		for _, cell := range cellList {
			cl = appendCellList(cl, kss.getLeafCellbyNode(cell, nodeName))
		}
	}
	return cl
}

// get all leaf cell according to the node
func (kss *KubeShareScheduler) getAllLeafCellbyNode(nodeName string) CellList {

	var cl CellList
	for _, freeList := range kss.cellFreeList {
		for _, cellList := range freeList {
			for _, cell := range cellList {
				cl = appendCellList(cl, kss.getLeafCellbyNode(cell, nodeName))
			}
		}
	}
	return cl
}

func (kss *KubeShareScheduler) getLeafCellbyNode(cell *Cell, nodeName string) CellList {

	node := cell.node

	if node != nodeName && node != "" {
		return nil
	}

	s := NewStack()
	var cellList CellList

	if cell.healthy {
		s.Push(cell)
	}

	for s.Len() > 0 {
		current := s.Pop()
		if current.level == 1 {
			kss.ksl.Debugf("getLeafCellbyNode: %+v", current)
			cellList = append(cellList, current)
		}

		node = current.node
		if node == nodeName || node == "" {
			child := current.child
			if child == nil {
				continue
			}
			for i := range child {
				if (node == nodeName || node == "") && child[i].healthy {

					s.Push(child[i])
				}
			}
		}
	}
	return cellList
}

// score = cell priority(computation power)
//       + gpu resource usage(defragmentation)
//       - # of free leaf cell (defragmentation)(%)
func (kss *KubeShareScheduler) calculateOpportunisticPodNodeScore(cellList CellList) int64 {
	if cellList == nil {
		return 0
	}
	// number of free leaf cells
	freeLeafCell := int64(0)
	score := int64(0)
	for _, cell := range cellList {
		//
		score += int64(kss.gpuPriority[cell.cellType])
		// gpu resource
		available := cell.available
		if available == 1 {
			freeLeafCell += 1
			// gpu usage : 0
		} else {
			// gpu usage
			score += int64((1 - cell.available) * 100)
		}
		kss.ksl.Debugf("OpportunisticPodNodeScore %v with score: %v", cell.cellType, score)
	}

	n := int64(len(cellList))
	score -= int64(freeLeafCell / n * 100) //
	return int64(score / n)
}
